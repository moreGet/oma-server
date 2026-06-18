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

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

// Ollama 스트리밍은 줄 단위 JSON(NDJSON)이다. 각 줄이 한 조각, done=true 가 마지막.
type ollamaChatChunk struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// ChatStream 은 Ollama /api/chat 을 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
func (a *OllamaAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	msgs := make([]ollamaMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, ollamaMessage{Role: string(m.Role), Content: m.Content})
	}
	payload := ollamaChatRequest{
		Model:    a.resolveModel(req.Model),
		Messages: msgs,
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
			return onChunk(domainllmprovider.ChatStreamChunk{Done: true, FinishReason: chunk.DoneReason, Usage: usage})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ollama: %w: read stream: %v", domainllmprovider.ErrUpstream, err)
	}
	// done 조각을 못 받고 스트림이 끝난 경우에도 종료 조각을 1회 보낸다.
	return onChunk(domainllmprovider.ChatStreamChunk{Done: true})
}
