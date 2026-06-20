package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*OllamaAdapter)(nil)

const (
	defaultOllamaEndpoint = "http://localhost:11434"
	defaultOllamaModel    = "llama3"
)

// OllamaAdapter 는 LOCAL provider 용 어댑터다(Ollama /api/chat 스트리밍 대상).
type OllamaAdapter struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewOllamaAdapter 는 도메인 ProviderConfig 로부터 OllamaAdapter 를 생성한다.
func NewOllamaAdapter(config domainllmprovider.ProviderConfig) *OllamaAdapter {
	return &OllamaAdapter{endpoint: config.Endpoint, model: config.Model, client: &http.Client{}}
}

// ProviderType 은 LOCAL 을 반환한다.
func (a *OllamaAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeLocal
}

func (a *OllamaAdapter) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultOllamaModel
}

func (a *OllamaAdapter) chatURL() string {
	ep := a.endpoint
	if ep == "" {
		ep = defaultOllamaEndpoint
	}
	return strings.TrimRight(ep, "/") + "/api/chat"
}

// --- Ollama 와이어 포맷 ---

type ollamaFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // Ollama 는 객체로 주고받음
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"` // "function"
	Function ollamaToolFunction `json:"function"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

// Ollama 스트리밍은 줄 단위 JSON(NDJSON). 각 줄이 한 조각, done=true 가 마지막.
type ollamaChatChunk struct {
	Message struct {
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

func toOllamaMessages(msgs []domainllmprovider.ChatMessage) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(msgs))
	for _, m := range msgs {
		om := ollamaMessage{Role: string(m.Role), Content: m.Content}
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{Function: ollamaFunctionCall{Name: tc.Name, Arguments: args}})
		}
		out = append(out, om)
	}
	return out
}

func toOllamaTools(tools []domainllmprovider.ToolDefinition) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ollamaTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, ollamaTool{
			Type:     "function",
			Function: ollamaToolFunction{Name: t.Name, Description: t.Description, Parameters: json.RawMessage(t.Parameters)},
		})
	}
	return out
}

// ChatStream 은 Ollama /api/chat 을 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
func (a *OllamaAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	payload := ollamaChatRequest{
		Model:    a.resolveModel(req.Model),
		Messages: toOllamaMessages(req.Messages),
		Tools:    toOllamaTools(req.Tools),
		Stream:   true,
	}
	if req.Temperature != nil || req.MaxTokens > 0 {
		payload.Options = &ollamaOptions{Temperature: req.Temperature, NumPredict: req.MaxTokens}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.chatURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama: %w: %v", domainllmprovider.ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ollama: %w: status %d: %s", domainllmprovider.ErrUpstream, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var collectedToolCalls []domainllmprovider.ToolCall
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		for _, tc := range chunk.Message.ToolCalls {
			collectedToolCalls = append(collectedToolCalls, domainllmprovider.ToolCall{
				Name:      tc.Function.Name,
				Arguments: string(tc.Function.Arguments),
			})
		}
		if chunk.Message.Content != "" {
			if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: chunk.Message.Content}); err != nil {
				return err
			}
		}
		if chunk.Done {
			usage := &domainllmprovider.ChatUsage{
				PromptTokens:     chunk.PromptEvalCount,
				CompletionTokens: chunk.EvalCount,
				TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
			}
			return onChunk(domainllmprovider.ChatStreamChunk{
				Done:         true,
				FinishReason: chunk.DoneReason,
				ToolCalls:    collectedToolCalls,
				Usage:        usage,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ollama: %w: read stream: %v", domainllmprovider.ErrUpstream, err)
	}
	return onChunk(domainllmprovider.ChatStreamChunk{Done: true, ToolCalls: collectedToolCalls})
}
