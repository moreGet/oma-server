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
	"sort"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*OpenAIAdapter)(nil)

const (
	defaultOpenAIEndpoint = "https://api.openai.com/v1"
	defaultOpenAIModel    = "gpt-4o-mini"
	openAIStreamBufferMax = 1 << 20 // SSE 한 줄 최대 1MiB
)

// OpenAIAdapter 는 OpenAI Chat Completions API(스트리밍, function-calling) 어댑터다.
type OpenAIAdapter struct {
	endpoint  string
	model     string
	apiKeyEnv string
	client    *http.Client
}

// NewOpenAIAdapter 는 도메인 ProviderConfig 로부터 OpenAIAdapter 를 생성한다.
// 스트리밍은 장시간 지속될 수 있으므로 client 전체 타임아웃은 두지 않고 ctx 로 취소를 제어한다.
func NewOpenAIAdapter(config domainllmprovider.ProviderConfig) *OpenAIAdapter {
	return &OpenAIAdapter{
		endpoint:  config.Endpoint,
		model:     config.Model,
		apiKeyEnv: config.APIKeyEnv,
		client:    &http.Client{},
	}
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *OpenAIAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

func (a *OpenAIAdapter) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultOpenAIModel
}

func (a *OpenAIAdapter) chatURL() string {
	ep := a.endpoint
	if ep == "" {
		ep = defaultOpenAIEndpoint
	}
	return strings.TrimRight(ep, "/") + "/chat/completions"
}

// --- OpenAI 와이어 포맷 ---

type openAIFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIFunctionCall `json:"function"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"` // "function"
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Stream        bool                 `json:"stream"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
	Tools         []openAITool         `json:"tools,omitempty"`
	ToolChoice    string               `json:"tool_choice,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// toolCallBuilder 는 스트리밍으로 조각조각 오는 tool_call 을 index 별로 누적한다.
type toolCallBuilder struct {
	id   string
	name string
	args strings.Builder
}

func toOpenAIMessages(msgs []domainllmprovider.ChatMessage) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		om := openAIMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		if len(m.ToolCalls) > 0 {
			om.ToolCalls = make([]openAIToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, openAIToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: openAIFunctionCall{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
		}
		out = append(out, om)
	}
	return out
}

func toOpenAITools(tools []domainllmprovider.ToolDefinition) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(t.Parameters),
			},
		})
	}
	return out
}

// ChatStream 은 OpenAI Chat Completions 를 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
// 도구가 있으면 function-calling 으로 전달하고, 스트리밍으로 오는 tool_call 조각을 누적해 최종 조각에 담는다.
func (a *OpenAIAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := ""
	if a.apiKeyEnv != "" {
		apiKey = os.Getenv(a.apiKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("openai: %w: API key not set (config api_key_env=%q)", domainllmprovider.ErrUpstream, a.apiKeyEnv)
	}

	payload := openAIChatRequest{
		Model:         a.resolveModel(req.Model),
		Messages:      toOpenAIMessages(req.Messages),
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
		Tools:         toOpenAITools(req.Tools),
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.chatURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openai: %w: %v", domainllmprovider.ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("openai: %w: status %d: %s", domainllmprovider.ErrUpstream, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var (
		finishReason string
		usage        *domainllmprovider.ChatUsage
		toolCalls    = map[int]*toolCallBuilder{}
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), openAIStreamBufferMax)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = &domainllmprovider.ChatUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		c := chunk.Choices[0]
		if c.FinishReason != nil && *c.FinishReason != "" {
			finishReason = *c.FinishReason
		}
		for _, tc := range c.Delta.ToolCalls {
			b := toolCalls[tc.Index]
			if b == nil {
				b = &toolCallBuilder{}
				toolCalls[tc.Index] = b
			}
			if tc.ID != "" {
				b.id = tc.ID
			}
			if tc.Function.Name != "" {
				b.name = tc.Function.Name
			}
			b.args.WriteString(tc.Function.Arguments)
		}
		if c.Delta.Content != "" {
			if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: c.Delta.Content}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("openai: %w: read stream: %v", domainllmprovider.ErrUpstream, err)
	}

	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    buildToolCalls(toolCalls),
		Usage:        usage,
	})
}

// buildToolCalls 는 index 순으로 정렬해 완성된 도구 호출 슬라이스를 만든다.
func buildToolCalls(m map[int]*toolCallBuilder) []domainllmprovider.ToolCall {
	if len(m) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(m))
	for i := range m {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]domainllmprovider.ToolCall, 0, len(idxs))
	for _, i := range idxs {
		b := m[i]
		out = append(out, domainllmprovider.ToolCall{ID: b.id, Name: b.name, Arguments: b.args.String()})
	}
	return out
}
