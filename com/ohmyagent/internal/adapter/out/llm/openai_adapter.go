package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*OpenAIAdapter)(nil)

const (
	// defaultOpenAIModel 은 req.Model / config.Model 둘 다 비었을 때의 기본 모델이다.
	defaultOpenAIModel = "gpt-4o-mini"
)

// OpenAIAdapter 는 OpenAI Go SDK 로 Chat Completions API(스트리밍, function-calling)를 호출하는 어댑터다.
// SSE 파싱·재시도·인증 헤더는 SDK 에 위임한다.
type OpenAIAdapter struct {
	endpoint  string // 빈 문자열이면 SDK 기본(api.openai.com) 사용
	model     string
	apiKey    string // 직접 저장된 키(복호화된 평문). 비면 apiKeyEnv 환경변수 사용
	apiKeyEnv string
	// reasoning 은 Provider 설정의 추론 강도(reasoning_effort)다. 빈 값이면 파라미터를 보내지 않는다.
	// 클라이언트 요청이 아니라 서버 설정에서만 온다.
	reasoning string
	// maxTokens 는 Provider 설정의 출력 토큰 상한이다(0 = 미지정).
	// 요청이 값을 주지 않을 때의 서버측 기본값으로 쓰인다.
	maxTokens int
	// useResponses 가 true 면 /v1/responses, 아니면 /v1/chat/completions 를 호출한다.
	useResponses bool
	httpClient   *http.Client // 공유 커넥션 풀(factory 가 주입)
}

// resolveMaxTokens 는 출력 토큰 상한을 정한다(요청 지정값 우선, 없으면 Provider 설정값).
// 0 을 반환하면 파라미터를 보내지 않는다(모델 기본값).
func (a *OpenAIAdapter) resolveMaxTokens(reqMaxTokens int) int {
	if reqMaxTokens > 0 {
		return reqMaxTokens
	}
	return a.maxTokens
}

// NewOpenAIAdapter 는 도메인 ProviderConfig 로부터 OpenAIAdapter 를 생성한다.
// SDK 클라이언트는 API 키가 필요하므로 호출 시점(ChatStream)에 생성하되, HTTP 커넥션 풀은 공유한다.
func NewOpenAIAdapter(config domainllmprovider.ProviderConfig, httpClient *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{
		endpoint:   config.Endpoint,
		model:      config.Model,
		apiKey:     config.APIKey,
		apiKeyEnv:  config.APIKeyEnv,
		reasoning:    config.Reasoning,
		maxTokens:    config.MaxTokens,
		useResponses: config.UsesResponsesAPI(),
		httpClient:   httpClient,
	}
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *OpenAIAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

// resolveModel 은 사용할 모델명을 정한다.
//
// 모델은 **서버(관리자)가 Provider 설정으로 정한다** — 클라이언트가 요청으로 바꿀 수 없다.
// reqModel 을 받는 시그니처는 유지하되 무시한다(호출부 변경 없이 정책을 한 곳에서 강제).
func (a *OpenAIAdapter) resolveModel(_ string) string {
	if a.model != "" {
		return a.model
	}
	return defaultOpenAIModel
}

// newClient 는 API 키와(설정 시) 사용자 지정 엔드포인트로 SDK 클라이언트를 만든다.
// 스트리밍은 장시간 지속될 수 있어 전역 HTTP 타임아웃을 두지 않고 ctx 로 취소를 제어한다.
func (a *OpenAIAdapter) newClient(apiKey string) openai.Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if a.httpClient != nil {
		opts = append(opts, option.WithHTTPClient(a.httpClient))
	}
	if a.endpoint != "" {
		opts = append(opts, option.WithBaseURL(a.endpoint))
	}
	return openai.NewClient(opts...)
}

// --- 도메인 → SDK 매핑 ---

// buildOpenAIMessages 는 도메인 ChatMessage 를 SDK 메시지 유니온으로 변환한다.
// system/user→텍스트, assistant→텍스트+tool_calls(히스토리 재생), tool→ToolCallID 로 묶인 실행 결과.
func buildOpenAIMessages(msgs []domainllmprovider.ChatMessage) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case domainllmprovider.ChatRoleSystem:
			out = append(out, openai.SystemMessage(m.Content))

		case domainllmprovider.ChatRoleUser:
			out = append(out, openai.UserMessage(m.Content))

		case domainllmprovider.ChatRoleAssistant:
			out = append(out, buildOpenAIAssistantMessage(m))

		case domainllmprovider.ChatRoleTool:
			// SDK 시그니처: ToolMessage(content, toolCallID).
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))

		default:
			// 알 수 없는 역할은 user 로 폴백(메시지 유실 방지).
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}

// buildOpenAIAssistantMessage 는 assistant 메시지를 만든다.
// ToolCalls 가 있으면 직접 유니온 구조체를 구성해 tool_calls 를 함께 싣는다.
func buildOpenAIAssistantMessage(m domainllmprovider.ChatMessage) openai.ChatCompletionMessageParamUnion {
	if len(m.ToolCalls) == 0 {
		return openai.AssistantMessage(m.Content)
	}

	toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: tc.Arguments, // 이미 JSON 문자열
				},
			},
		})
	}

	assistant := openai.ChatCompletionAssistantMessageParam{
		ToolCalls: toolCalls,
	}
	if m.Content != "" {
		assistant.Content.OfString = openai.String(m.Content)
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}

// buildOpenAITools 는 도메인 ToolDefinition 슬라이스를 SDK function 도구로 변환한다.
// Parameters 는 raw JSON Schema(object) 바이트이며 FunctionParameters(=map[string]any)로 언마샬한다.
func buildOpenAITools(tools []domainllmprovider.ToolDefinition) []openai.ChatCompletionToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		def := openai.FunctionDefinitionParam{Name: t.Name}
		if t.Description != "" {
			def.Description = openai.String(t.Description)
		}
		if len(t.Parameters) > 0 {
			var params openai.FunctionParameters // map[string]any
			if err := json.Unmarshal(t.Parameters, &params); err == nil {
				def.Parameters = params
			}
		}
		out = append(out, openai.ChatCompletionFunctionTool(def))
	}
	return out
}

// --- 스트리밍 누적 ---

// openAIToolCallAccumulator 는 스트리밍으로 조각조각 도착하는 tool_call 을
// index 별로 누적한다(id/name 는 첫 조각, arguments 는 조각들을 이어 붙임).
type openAIToolCallAccumulator struct {
	id   string
	name string
	args strings.Builder
}

// collectOpenAIToolCalls 는 index 순으로 정렬해 완성된 도구 호출 슬라이스를 만든다.
func collectOpenAIToolCalls(m map[int64]*openAIToolCallAccumulator) []domainllmprovider.ToolCall {
	if len(m) == 0 {
		return nil
	}
	idxs := make([]int64, 0, len(m))
	for i := range m {
		idxs = append(idxs, i)
	}
	sort.Slice(idxs, func(a, b int) bool { return idxs[a] < idxs[b] })

	out := make([]domainllmprovider.ToolCall, 0, len(idxs))
	for _, i := range idxs {
		b := m[i]
		out = append(out, domainllmprovider.ToolCall{
			ID:        b.id,
			Name:      b.name,
			Arguments: b.args.String(),
		})
	}
	return out
}

// buildParams 는 도메인 요청 + Provider 설정을 SDK 요청 파라미터로 조립한다.
// 전송 바이트가 계약이므로 ChatStream 에서 분리해 직렬화 결과를 단위 테스트한다.
func (a *OpenAIAdapter) buildParams(req domainllmprovider.ChatRequest) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    a.resolveModel(req.Model),
		Messages: buildOpenAIMessages(req.Messages),
		// 마지막 청크에 usage 를 포함시킨다.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if tools := buildOpenAITools(req.Tools); len(tools) > 0 {
		params.Tools = tools
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		}
	}
	if mt := a.resolveMaxTokens(req.MaxTokens); mt > 0 {
		params.MaxCompletionTokens = openai.Int(int64(mt))
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	// 추론 강도는 Provider 설정에서만 온다(클라이언트가 지정할 수 없다).
	// 빈 값이면 파라미터를 아예 보내지 않아 모델의 기본 동작을 그대로 둔다.
	//
	// 주의: /v1/chat/completions 에서는 function tools 와 reasoning_effort 를 함께 쓸 수 없는
	// 모델이 있다(gpt-5.6-luna 등 → 400). 그런 모델로 도구를 쓰려면 reasoning 을 "none" 으로
	// 두거나 /v1/responses 로 옮겨야 한다. 서버는 조합을 추측해 고치지 않고 그대로 보낸다.
	if a.reasoning != "" {
		params.ReasoningEffort = shared.ReasoningEffort(a.reasoning)
	}
	return params
}

// ChatStream 은 OpenAI Chat Completions 를 스트리밍으로 호출하고 응답 조각을 onChunk 로 전달한다.
// 도구가 있으면 function-calling 으로 넘기고, 스트리밍으로 오는 tool_call 조각을 누적해
// 마지막 Done 조각에 담는다. onChunk 가 에러를 반환하면 스트리밍을 중단하고 그 에러를 반환한다.
func (a *OpenAIAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	// /v1/responses 는 추론과 도구를 동시에 지원한다(chat/completions 는 모델에 따라 400).
	if a.useResponses {
		return a.chatStreamResponses(ctx, req, onChunk)
	}

	apiKey, err := requireAPIKey("openai", a.apiKey, a.apiKeyEnv)
	if err != nil {
		return err
	}

	client := a.newClient(apiKey)

	stream := client.Chat.Completions.NewStreaming(ctx, a.buildParams(req))
	defer func() { _ = stream.Close() }()

	var (
		finishReason string
		usage        *domainllmprovider.ChatUsage
		toolCalls    = map[int64]*openAIToolCallAccumulator{}
	)

	for stream.Next() {
		chunk := stream.Current()

		// usage 는 IncludeUsage=true 일 때 보통 마지막 청크에서만 채워진다.
		if chunk.Usage.TotalTokens > 0 ||
			chunk.Usage.PromptTokens > 0 ||
			chunk.Usage.CompletionTokens > 0 {
			usage = &domainllmprovider.ChatUsage{
				PromptTokens:     int(chunk.Usage.PromptTokens),
				CompletionTokens: int(chunk.Usage.CompletionTokens),
				TotalTokens:      int(chunk.Usage.TotalTokens),
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}

		// 스트리밍 tool_call 조각을 index 별로 누적한다.
		for _, tc := range choice.Delta.ToolCalls {
			b := toolCalls[tc.Index]
			if b == nil {
				b = &openAIToolCallAccumulator{}
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

		// 증분 텍스트를 즉시 전달한다.
		if choice.Delta.Content != "" {
			if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: choice.Delta.Content}); err != nil {
				return err
			}
		}
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("openai: %w: %v", domainllmprovider.ErrUpstream, err)
	}

	// 스트림 종료 후 마지막 조각을 정확히 1회 전달한다.
	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    collectOpenAIToolCalls(toolCalls),
		Usage:        usage,
	})
}
