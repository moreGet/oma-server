package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ollama/ollama/api"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*OllamaAdapter)(nil)

// defaultOllamaModel 은 요청/설정 모두 모델을 지정하지 않았을 때의 기본 모델이다.
// endpoint 기본값은 SDK 의 ClientFromEnvironment(OLLAMA_HOST → http://localhost:11434)가 처리한다.
const defaultOllamaModel = "llama3"

// OllamaAdapter 는 Ollama Go SDK 의 Chat 스트리밍 API 를 쓰는 LOCAL provider 어댑터다.
// 로컬 LLM 이므로 API 키는 사용하지 않는다.
type OllamaAdapter struct {
	endpoint   string       // 사용자 지정 엔드포인트("" 이면 환경변수/기본값)
	model      string       // Provider 기본 모델("" 이면 defaultOllamaModel)
	httpClient *http.Client // 공유 커넥션 풀(factory 가 주입)
}

// NewOllamaAdapter 는 도메인 ProviderConfig 로부터 OllamaAdapter 를 생성한다.
func NewOllamaAdapter(config domainllmprovider.ProviderConfig, httpClient *http.Client) *OllamaAdapter {
	return &OllamaAdapter{endpoint: config.Endpoint, model: config.Model, httpClient: httpClient}
}

// ProviderType 은 LOCAL 을 반환한다.
func (a *OllamaAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeLocal
}

// resolveModel 은 모델명을 결정한다.
// 모델은 서버(관리자)의 Provider 설정으로만 정해지며 클라이언트 요청은 무시한다.
func (a *OllamaAdapter) resolveModel(_ string) string {
	if a.model != "" {
		return a.model
	}
	return defaultOllamaModel
}

// newOllamaClient 는 어댑터 설정에 맞는 SDK 클라이언트를 만든다.
// endpoint 가 있으면 그 URL 로, 없으면 ClientFromEnvironment(OLLAMA_HOST, 기본 http://localhost:11434)로 만든다.
func (a *OllamaAdapter) newOllamaClient() (*api.Client, error) {
	hc := a.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if a.endpoint != "" {
		u, err := url.Parse(a.endpoint)
		if err != nil {
			return nil, fmt.Errorf("ollama: %w: parse endpoint %q: %v", domainllmprovider.ErrUpstream, a.endpoint, err)
		}
		return api.NewClient(u, hc), nil
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("ollama: %w: client from environment: %v", domainllmprovider.ErrUpstream, err)
	}
	return client, nil
}

// buildOllamaMessages 는 도메인 메시지를 SDK 메시지로 변환한다(assistant+ToolCalls 는 히스토리 재생용).
// tool 역할은 role="tool"+ToolName/ToolCallID 로 실행 결과를 식별한다.
func buildOllamaMessages(msgs []domainllmprovider.ChatMessage) ([]api.Message, error) {
	out := make([]api.Message, 0, len(msgs))
	for _, m := range msgs {
		om := api.Message{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolName:   m.Name,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			args, err := decodeOllamaToolArguments(tc.Arguments)
			if err != nil {
				return nil, err
			}
			om.ToolCalls = append(om.ToolCalls, api.ToolCall{
				ID: tc.ID,
				Function: api.ToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		out = append(out, om)
	}
	return out, nil
}

// decodeOllamaToolArguments 는 Arguments(JSON 문자열)를 SDK 의 ToolCallFunctionArguments 로 변환한다.
// 이 타입은 UnmarshalJSON 을 구현하므로 json.Unmarshal 로 채울 수 있다.
func decodeOllamaToolArguments(arguments string) (api.ToolCallFunctionArguments, error) {
	args := api.NewToolCallFunctionArguments()
	raw := arguments
	if raw == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return args, fmt.Errorf("ollama: %w: decode tool arguments: %v", domainllmprovider.ErrUpstream, err)
	}
	return args, nil
}

// buildOllamaTools 는 도메인 도구 정의를 SDK 의 api.Tools 로 변환한다.
// Parameters 는 JSON Schema(object) 원문 바이트이며, SDK 의 ToolFunctionParameters 로 역직렬화한다.
func buildOllamaTools(tools []domainllmprovider.ToolDefinition) (api.Tools, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make(api.Tools, 0, len(tools))
	for _, t := range tools {
		fn := api.ToolFunction{Name: t.Name, Description: t.Description}
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &fn.Parameters); err != nil {
				return nil, fmt.Errorf("ollama: %w: decode tool parameters for %q: %v", domainllmprovider.ErrUpstream, t.Name, err)
			}
		}
		out = append(out, api.Tool{Type: "function", Function: fn})
	}
	return out, nil
}

// buildOllamaOptions 는 temperature/num_predict 옵션 맵을 만든다(미지정 항목은 생략).
func buildOllamaOptions(req domainllmprovider.ChatRequest) map[string]any {
	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	return opts
}

// encodeOllamaToolArguments 는 SDK 의 ToolCallFunctionArguments 를 도메인용 JSON 문자열로 직렬화한다.
// ToolCallFunctionArguments 는 MarshalJSON 을 구현하므로 항상 유효한 JSON("{}" 포함)이 나온다.
func encodeOllamaToolArguments(args api.ToolCallFunctionArguments) string {
	b, err := json.Marshal(args)
	if err != nil || len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// ChatStream 은 SDK Chat 을 stream=true 로 호출해 각 청크 텍스트는 즉시 onChunk(Delta)로 흘리고 ToolCalls/DoneReason/usage 는 누적한다.
// client.Chat 은 블로킹이므로 반환 후 Done=true 최종 조각을 정확히 1회 전송한다(onChunk 에러 시 중단).
func (a *OllamaAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	client, err := a.newOllamaClient()
	if err != nil {
		return err
	}

	messages, err := buildOllamaMessages(req.Messages)
	if err != nil {
		return err
	}
	tools, err := buildOllamaTools(req.Tools)
	if err != nil {
		return err
	}

	stream := true
	chatReq := &api.ChatRequest{
		Model:    a.resolveModel(req.Model),
		Messages: messages,
		Tools:    tools,
		Stream:   &stream,
		Options:  buildOllamaOptions(req),
	}

	// 콜백에서 누적할 최종 상태.
	var (
		collectedToolCalls []domainllmprovider.ToolCall
		finishReason       string
		promptTokens       int
		completionTokens   int
		sawDone            bool
		callbackErr        error // onChunk 가 돌려준 사용자측 중단 에러
	)

	fn := func(resp api.ChatResponse) error {
		for _, tc := range resp.Message.ToolCalls {
			collectedToolCalls = append(collectedToolCalls, domainllmprovider.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: encodeOllamaToolArguments(tc.Function.Arguments),
			})
		}
		if resp.Message.Content != "" {
			if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: resp.Message.Content}); err != nil {
				callbackErr = err
				return err // 스트리밍 중단
			}
		}
		if resp.Done {
			sawDone = true
			finishReason = resp.DoneReason
			promptTokens = resp.PromptEvalCount
			completionTokens = resp.EvalCount
		}
		return nil
	}

	if err := client.Chat(ctx, chatReq, fn); err != nil {
		// onChunk 가 반환한 중단 에러는 도메인 계약상 그대로 전파한다(업스트림 래핑하지 않음).
		if callbackErr != nil {
			return callbackErr
		}
		return fmt.Errorf("ollama: %w: %v", domainllmprovider.ErrUpstream, err)
	}

	// 최종 Done 조각을 정확히 1회 전송한다.
	chunk := domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    collectedToolCalls,
	}
	if sawDone {
		chunk.Usage = &domainllmprovider.ChatUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
	}
	return onChunk(chunk)
}
