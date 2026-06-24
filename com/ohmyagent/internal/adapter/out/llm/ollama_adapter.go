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

const (
	// defaultOllamaEndpoint 는 endpoint 미설정 시 사용되는 로컬 Ollama 주소다.
	defaultOllamaEndpoint = "http://localhost:11434"
	// defaultOllamaModel 은 요청/설정 모두 모델을 지정하지 않았을 때의 기본 모델이다.
	defaultOllamaModel = "llama3"
)

// OllamaAdapter 는 LOCAL provider 용 어댑터다.
// 공식 Ollama Go SDK(github.com/ollama/ollama/api)의 Chat 스트리밍 API 를 사용한다.
// 로컬 LLM 이므로 API 키는 사용하지 않는다.
type OllamaAdapter struct {
	endpoint string // 사용자 지정 엔드포인트("" 이면 환경변수/기본값)
	model    string // Provider 기본 모델("" 이면 defaultOllamaModel)
}

// NewOllamaAdapter 는 도메인 ProviderConfig 로부터 OllamaAdapter 를 생성한다.
func NewOllamaAdapter(config domainllmprovider.ProviderConfig) *OllamaAdapter {
	return &OllamaAdapter{endpoint: config.Endpoint, model: config.Model}
}

// ProviderType 은 LOCAL 을 반환한다.
func (a *OllamaAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeLocal
}

// resolveModel 은 요청 모델 → 어댑터 기본 모델 → 패키지 기본 모델 순으로 모델명을 결정한다.
func (a *OllamaAdapter) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultOllamaModel
}

// newOllamaClient 는 어댑터 설정에 맞는 SDK 클라이언트를 만든다.
//   - endpoint 가 지정되면 해당 URL 로 api.NewClient 를 만든다.
//   - 비어 있으면 api.ClientFromEnvironment 로 OLLAMA_HOST(기본 http://localhost:11434)를 사용한다.
func (a *OllamaAdapter) newOllamaClient() (*api.Client, error) {
	if a.endpoint != "" {
		u, err := url.Parse(a.endpoint)
		if err != nil {
			return nil, fmt.Errorf("ollama: %w: parse endpoint %q: %v", domainllmprovider.ErrUpstream, a.endpoint, err)
		}
		return api.NewClient(u, http.DefaultClient), nil
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("ollama: %w: client from environment: %v", domainllmprovider.ErrUpstream, err)
	}
	return client, nil
}

// buildOllamaMessages 는 도메인 메시지를 SDK 메시지로 변환한다.
//   - assistant + ToolCalls: 모델이 만든 도구 호출을 히스토리로 재생한다.
//     도메인 Arguments(JSON 문자열)를 SDK 의 ToolCallFunctionArguments 로 역직렬화한다.
//   - tool 역할: 도구 실행 결과. Ollama 는 role="tool" 을 사용하며 ToolName/ToolCallID 로 어떤 호출의 결과인지 식별한다.
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

// decodeOllamaToolArguments 는 도메인 Arguments(JSON 문자열)를 SDK 의
// ToolCallFunctionArguments(불투명 ordered-map 타입)로 변환한다.
// ToolCallFunctionArguments 는 UnmarshalJSON 을 구현하므로 json.Unmarshal 로 채울 수 있다.
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

// ChatStream 은 공식 SDK 의 Chat 을 stream=true 로 호출하고 응답 조각을 onChunk 로 전달한다.
//
// 동작 계약:
//   - 콜백에서 들어오는 각 청크의 텍스트(Content)는 즉시 onChunk(Delta) 로 흘려보낸다.
//     onChunk 가 에러를 반환하면 그 에러를 콜백에서 반환해 스트리밍을 중단한다.
//   - 도구 호출(ToolCalls)과 완료 메타(DoneReason/usage)는 콜백 내부에서 누적만 한다.
//   - client.Chat 은 스트림이 끝날 때까지 블로킹하므로, 반환된 뒤에 Done=true 최종 조각을 정확히 1회 전송한다.
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
