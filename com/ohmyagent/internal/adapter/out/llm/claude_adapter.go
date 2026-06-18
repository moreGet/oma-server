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

// ClaudeAdapter 는 Anthropic Messages API(스트리밍) 어댑터다.
// 시크릿은 DB 가 아니라 환경변수에서 읽는다(config.APIKeyEnv 가 환경변수 이름).
type ClaudeAdapter struct {
	endpoint  string
	model     string
	apiKeyEnv string
	client    *http.Client
}

// NewClaudeAdapter 는 도메인 ProviderConfig 로부터 ClaudeAdapter 를 생성한다.
// 스트리밍은 장시간 지속될 수 있으므로 client 전체 타임아웃은 두지 않고 ctx 로 취소를 제어한다.
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

type anthropicMessage struct {
	Role    string `json:"role"` // user | assistant (system 은 별도 필드)
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
}

// anthropicStreamEvent 는 SSE data 라인의 JSON 을 type 으로 분기 파싱하기 위한 통합 구조다.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type       string `json:"type"` // text_delta 등
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
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

// splitSystem 은 system 역할 메시지를 top-level system 텍스트로 분리하고,
// 나머지(user/assistant)만 messages 로 반환한다(Anthropic 은 messages 에 system 역할 불가).
func splitSystem(msgs []domainllmprovider.ChatMessage) (system string, rest []anthropicMessage) {
	var systems []string
	rest = make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == domainllmprovider.ChatRoleSystem {
			systems = append(systems, m.Content)
			continue
		}
		rest = append(rest, anthropicMessage{Role: string(m.Role), Content: m.Content})
	}
	return strings.Join(systems, "\n\n"), rest
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

	system, messages := splitSystem(req.Messages)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClaudeMaxTokens
	}
	payload := anthropicRequest{
		Model:       a.resolveModel(req.Model),
		MaxTokens:   maxTokens,
		Messages:    messages,
		System:      system,
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
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), claudeStreamBufferMax)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue // event:/ping/빈 줄은 무시(data 의 type 으로 분기)
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			promptTokens = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: ev.Delta.Text}); err != nil {
					return err
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
			// 스트림 종료. 루프 종료 후 종료 조각 전송.
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
	return onChunk(domainllmprovider.ChatStreamChunk{Done: true, FinishReason: finishReason, Usage: usage})
}
