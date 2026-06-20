package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*ClaudeAdapter)(nil)

const (
	defaultAnthropicEndpoint = "https://api.anthropic.com"
	defaultClaudeModel       = "claude-3-5-sonnet-latest"
	defaultClaudeMaxTokens   = 1024 // Anthropic 은 max_tokens 필수 → 미지정 시 기본값
	anthropicVersion         = "2023-06-01"
	claudeStreamBufferMax    = 1 << 20
)

// ClaudeAdapter 는 Anthropic Messages API(스트리밍, tool use) 어댑터다.
type ClaudeAdapter struct {
	endpoint  string
	model     string
	apiKeyEnv string
	client    *http.Client
}

// NewClaudeAdapter 는 도메인 ProviderConfig 로부터 ClaudeAdapter 를 생성한다.
func NewClaudeAdapter(config domainllmprovider.ProviderConfig) *ClaudeAdapter {
	return &ClaudeAdapter{
		endpoint:  config.Endpoint,
		model:     config.Model,
		apiKeyEnv: config.APIKeyEnv,
		client:    &http.Client{},
	}
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *ClaudeAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

func (a *ClaudeAdapter) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultClaudeModel
}

func (a *ClaudeAdapter) messagesURL() string {
	ep := a.endpoint
	if ep == "" {
		ep = defaultAnthropicEndpoint
	}
	return strings.TrimRight(ep, "/") + "/v1/messages"
}

// --- Anthropic 와이어 포맷 ---

type anthropicContentBlock struct {
	Type      string          `json:"type"`                  // text | tool_use | tool_result
	Text      string          `json:"text,omitempty"`        // text
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   string          `json:"content,omitempty"`     // tool_result
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string 또는 []anthropicContentBlock
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
}

// anthropicStreamEvent 는 SSE data 라인 JSON 을 type 으로 분기 파싱하기 위한 통합 구조다.
type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// splitSystem 은 system 역할 메시지를 top-level system 텍스트로 분리한다.
func splitSystem(msgs []domainllmprovider.ChatMessage) string {
	var systems []string
	for _, m := range msgs {
		if m.Role == domainllmprovider.ChatRoleSystem {
			systems = append(systems, m.Content)
		}
	}
	return strings.Join(systems, "\n\n")
}

// toAnthropicMessages 는 도메인 메시지를 Anthropic 메시지로 변환한다.
//   - tool 역할(결과)들은 연속 병합되어 하나의 user 메시지(tool_result 블록 배열)로,
//   - assistant + ToolCalls 는 tool_use 블록 배열로 재구성한다(멀티턴 루프 히스토리).
func toAnthropicMessages(msgs []domainllmprovider.ChatMessage) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	var pendingToolResults []anthropicContentBlock
	flush := func() {
		if len(pendingToolResults) > 0 {
			out = append(out, anthropicMessage{Role: "user", Content: pendingToolResults})
			pendingToolResults = nil
		}
	}
	for _, m := range msgs {
		switch m.Role {
		case domainllmprovider.ChatRoleSystem:
			continue
		case domainllmprovider.ChatRoleTool:
			pendingToolResults = append(pendingToolResults, anthropicContentBlock{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
			})
		case domainllmprovider.ChatRoleAssistant:
			flush()
			if len(m.ToolCalls) > 0 {
				blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
				if m.Content != "" {
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					input := json.RawMessage(tc.Arguments)
					if len(input) == 0 {
						input = json.RawMessage("{}")
					}
					blocks = append(blocks, anthropicContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
				}
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			} else {
				out = append(out, anthropicMessage{Role: "assistant", Content: m.Content})
			}
		default: // user
			flush()
			out = append(out, anthropicMessage{Role: "user", Content: m.Content})
		}
	}
	flush()
	return out
}

func toAnthropicTools(tools []domainllmprovider.ToolDefinition) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		schema := json.RawMessage(t.Parameters)
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out
}

// ChatStream 은 Anthropic Messages API 를 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
func (a *ClaudeAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := ""
	if a.apiKeyEnv != "" {
		apiKey = os.Getenv(a.apiKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("claude: %w: API key not set (config api_key_env=%q)", domainllmprovider.ErrUpstream, a.apiKeyEnv)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClaudeMaxTokens
	}
	payload := anthropicRequest{
		Model:       a.resolveModel(req.Model),
		MaxTokens:   maxTokens,
		Messages:    toAnthropicMessages(req.Messages),
		System:      splitSystem(req.Messages),
		Tools:       toAnthropicTools(req.Tools),
		Stream:      true,
		Temperature: req.Temperature,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("claude: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("claude: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("claude: %w: %v", domainllmprovider.ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("claude: %w: status %d: %s", domainllmprovider.ErrUpstream, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var (
		finishReason     string
		promptTokens     int
		completionTokens int
		toolBlocks       = map[int]*toolCallBuilder{} // openai_adapter.go 정의 재사용
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), claudeStreamBufferMax)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			promptTokens = ev.Message.Usage.InputTokens
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				toolBlocks[ev.Index] = &toolCallBuilder{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: ev.Delta.Text}); err != nil {
						return err
					}
				}
			case "input_json_delta":
				if b := toolBlocks[ev.Index]; b != nil {
					b.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				finishReason = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens > 0 {
				completionTokens = ev.Usage.OutputTokens
			}
		case "error":
			return fmt.Errorf("claude: %w: %s", domainllmprovider.ErrUpstream, ev.Error.Message)
		case "message_stop":
			// 종료. 루프 후 종료 조각 전송.
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("claude: %w: read stream: %v", domainllmprovider.ErrUpstream, err)
	}

	usage := &domainllmprovider.ChatUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    buildToolCalls(toolBlocks), // openai_adapter.go 의 헬퍼 재사용
		Usage:        usage,
	})
}
