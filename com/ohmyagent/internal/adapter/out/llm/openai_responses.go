package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// /v1/responses 경로. /v1/chat/completions 와 달리 추론(reasoning)과 도구(function tools)를
// 동시에 쓸 수 있다 — chat/completions 는 모델에 따라 이 조합을 400 으로 거부한다.
//
// 구조적 차이 두 가지가 이 파일 대부분을 설명한다:
//  1. 도구 호출이 assistant 메시지 안에 중첩되지 않고 **형제 입력 아이템**으로 들어간다.
//  2. 인자 델타는 call_id 가 아니라 item_id 로 온다. 클라이언트에 돌려줘야 하는 call_id 는
//     response.output_item.added 에서만 알 수 있으므로 item_id → call_id 매핑을 유지해야 한다.

// SSE 이벤트 타입(문자열 스위치가 유니온 재파싱보다 싸다).
const (
	respEventOutputTextDelta   = "response.output_text.delta"
	respEventFuncArgsDelta     = "response.function_call_arguments.delta"
	respEventOutputItemAdded   = "response.output_item.added"
	respEventReasoningSummary  = "response.reasoning_summary_text.delta"
	respEventReasoningText     = "response.reasoning_text.delta"
	respEventCompleted         = "response.completed"
	respEventIncomplete        = "response.incomplete"
	respEventFailed            = "response.failed"
	respEventError             = "error"
	respItemTypeFunctionCall   = "function_call"
	respIncompleteMaxOutTokens = "max_output_tokens"
)

// buildResponsesInput 은 도메인 대화 이력을 Responses 입력 아이템 목록으로 변환한다.
//
// assistant 턴에 도구 호출이 있으면 텍스트 아이템과 별개로 호출마다 function_call 아이템을
// 하나씩 만든다(중첩이 아니라 형제). tool 결과는 call_id 로만 묶이며 role/name 을 갖지 않는다.
func buildResponsesInput(msgs []domainllmprovider.ChatMessage) responses.ResponseInputParam {
	out := make(responses.ResponseInputParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case domainllmprovider.ChatRoleSystem:
			out = append(out, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleSystem))

		case domainllmprovider.ChatRoleUser:
			out = append(out, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleUser))

		case domainllmprovider.ChatRoleAssistant:
			if m.Content != "" {
				out = append(out, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range m.ToolCalls {
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(tc.Arguments, tc.ID, tc.Name))
			}

		case domainllmprovider.ChatRoleTool:
			out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(m.ToolCallID, m.Content))

		default:
			// 알 수 없는 역할은 user 로 폴백(메시지 유실 방지) — chat/completions 경로와 동일.
			out = append(out, responses.ResponseInputItemParamOfMessage(m.Content, responses.EasyInputMessageRoleUser))
		}
	}
	return out
}

// buildResponsesTools 는 도메인 도구 정의를 Responses function 도구로 변환한다.
func buildResponsesTools(tools []domainllmprovider.ToolDefinition) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		fn := &responses.FunctionToolParam{Name: t.Name}
		if t.Description != "" {
			fn.Description = openai.String(t.Description)
		}
		if len(t.Parameters) > 0 {
			var params map[string]any
			if err := json.Unmarshal(t.Parameters, &params); err == nil {
				fn.Parameters = params
			}
		}
		out = append(out, responses.ToolUnionParam{OfFunction: fn})
	}
	return out
}

// buildResponsesParams 는 도메인 요청 + Provider 설정을 Responses 요청 파라미터로 조립한다.
// 전송 바이트가 계약이므로 스트리밍에서 분리해 직렬화 결과를 단위 테스트한다.
func (a *OpenAIAdapter) buildResponsesParams(req domainllmprovider.ChatRequest) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(a.resolveModel(req.Model)),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: buildResponsesInput(req.Messages),
		},
	}
	if tools := buildResponsesTools(req.Tools); len(tools) > 0 {
		params.Tools = tools
		params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
	}
	if mt := a.resolveMaxTokens(req.MaxTokens); mt > 0 {
		params.MaxOutputTokens = openai.Int(int64(mt))
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	// 추론 강도는 Provider 설정에서만 온다(클라이언트가 지정할 수 없다).
	// chat/completions 와 달리 여기서는 도구와 함께 써도 거부되지 않는다.
	if a.reasoning != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(a.reasoning)}
	}
	return params
}

// responsesToolCallBuilder 는 스트리밍으로 조각나 오는 도구 호출을 item_id 별로 누적한다.
type responsesToolCallBuilder struct {
	callID string
	name   string
	order  int64 // output_index — 방출 순서를 모델이 만든 순서와 맞추기 위함
	args   strings.Builder
}

// collectResponsesToolCalls 는 output_index 순으로 완성된 도구 호출 슬라이스를 만든다.
func collectResponsesToolCalls(m map[string]*responsesToolCallBuilder) []domainllmprovider.ToolCall {
	if len(m) == 0 {
		return nil
	}
	builders := make([]*responsesToolCallBuilder, 0, len(m))
	for _, b := range m {
		builders = append(builders, b)
	}
	sort.Slice(builders, func(i, j int) bool { return builders[i].order < builders[j].order })

	out := make([]domainllmprovider.ToolCall, 0, len(builders))
	for _, b := range builders {
		out = append(out, domainllmprovider.ToolCall{
			ID:        b.callID,
			Name:      b.name,
			Arguments: b.args.String(),
		})
	}
	return out
}

// chatStreamResponses 는 /v1/responses 를 스트리밍 호출하고 응답 조각을 onChunk 로 전달한다.
// ChatStream 과 동일한 계약을 지킨다(마지막에 Done=true 조각 정확히 1회).
func (a *OpenAIAdapter) chatStreamResponses(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := resolveAPIKey(a.apiKey, a.apiKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("openai: %w: API key not set (set config api_key or api_key_env)", domainllmprovider.ErrUpstream)
	}

	client := a.newClient(apiKey)
	stream := client.Responses.NewStreaming(ctx, a.buildResponsesParams(req))
	defer func() { _ = stream.Close() }()

	var (
		finishReason string
		usage        *domainllmprovider.ChatUsage
		toolCalls    = map[string]*responsesToolCallBuilder{}
	)

	for stream.Next() {
		event := stream.Current()

		switch event.Type {
		case respEventOutputItemAdded:
			// 도구 호출의 call_id·name 은 이 이벤트에서만 알 수 있다(인자 델타는 item_id 로만 온다).
			if event.Item.Type == respItemTypeFunctionCall {
				toolCalls[event.Item.ID] = &responsesToolCallBuilder{
					callID: event.Item.CallID,
					name:   event.Item.Name,
					order:  event.OutputIndex,
				}
			}

		case respEventFuncArgsDelta:
			if b := toolCalls[event.ItemID]; b != nil {
				b.args.WriteString(event.Delta)
			}

		case respEventOutputTextDelta:
			if event.Delta != "" {
				if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: event.Delta}); err != nil {
					return err
				}
			}

		case respEventReasoningSummary, respEventReasoningText:
			// 추론 요약은 확장 사고와 같은 채널로 흘려보낸다(클라이언트가 이미 처리하는 경로).
			if event.Delta != "" {
				if err := onChunk(domainllmprovider.ChatStreamChunk{ThinkingDelta: event.Delta}); err != nil {
					return err
				}
			}

		case respEventCompleted:
			usage = responsesUsage(event.Response.Usage)

		case respEventIncomplete:
			usage = responsesUsage(event.Response.Usage)
			// 토큰 상한으로 잘린 경우를 클라이언트 어휘(max_tokens)로 정규화할 수 있게 원문을 싣는다.
			if event.Response.IncompleteDetails.Reason == respIncompleteMaxOutTokens {
				finishReason = "max_tokens"
			}

		case respEventFailed:
			if msg := event.Response.Error.Message; msg != "" {
				return fmt.Errorf("openai responses: %w: %s", domainllmprovider.ErrUpstream, msg)
			}
			return fmt.Errorf("openai responses: %w: response failed", domainllmprovider.ErrUpstream)

		case respEventError:
			return fmt.Errorf("openai responses: %w: %s", domainllmprovider.ErrUpstream, event.Message)
		}
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("openai responses: %w: %v", domainllmprovider.ErrUpstream, err)
	}

	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: finishReason,
		ToolCalls:    collectResponsesToolCalls(toolCalls),
		Usage:        usage,
	})
}

// responsesUsage 는 Responses 사용량을 도메인 타입으로 변환한다.
// 필드명이 chat/completions 와 다르다(prompt→input, completion→output).
func responsesUsage(u responses.ResponseUsage) *domainllmprovider.ChatUsage {
	if u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return &domainllmprovider.ChatUsage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.TotalTokens),
		CacheReadTokens:  int(u.InputTokensDetails.CachedTokens),
	}
}
