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
var _ domainllmprovider.Adapter = (*OpenAIAdapter)(nil)

const (
	defaultOpenAIEndpoint = "https://api.openai.com/v1"
	defaultOpenAIModel    = "gpt-4o-mini"
	openAIStreamBufferMax = 1 << 20 // SSE 한 줄 최대 1MiB
)

// OpenAIAdapter 는 OpenAI Chat Completions API(스트리밍) 어댑터다.
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

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Stream        bool                 `json:"stream"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ChatStream 은 OpenAI Chat Completions 를 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
func (a *OpenAIAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := ""
	if a.apiKeyEnv != "" {
		apiKey = os.Getenv(a.apiKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("openai: %w: API key not set (config api_key_env=%q)", domainllmprovider.ErrUpstream, a.apiKeyEnv)
	}

	msgs := make([]openAIMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: string(m.Role), Content: m.Content})
	}
	payload := openAIChatRequest{
		Model:         a.resolveModel(req.Model),
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
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
			// 비정상 조각은 건너뛴다(주석/keep-alive 등).
			continue
		}
		if chunk.Usage != nil {
			usage = &domainllmprovider.ChatUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		if len(chunk.Choices) > 0 {
			c := chunk.Choices[0]
			if c.FinishReason != nil && *c.FinishReason != "" {
				finishReason = *c.FinishReason
			}
			if c.Delta.Content != "" {
				if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: c.Delta.Content}); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("openai: %w: read stream: %v", domainllmprovider.ErrUpstream, err)
	}

	return onChunk(domainllmprovider.ChatStreamChunk{Done: true, FinishReason: finishReason, Usage: usage})
}
