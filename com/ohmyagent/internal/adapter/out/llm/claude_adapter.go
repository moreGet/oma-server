package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Adapter = (*ClaudeAdapter)(nil)

const (
	// defaultClaudeModel 은 요청·설정 모두 모델을 비웠을 때의 기본 Anthropic 모델이다.
	defaultClaudeModel = "claude-3-5-sonnet-latest"
	// defaultClaudeMaxTokens 는 max_tokens 미지정 시 기본값이다(Anthropic 은 max_tokens 필수).
	defaultClaudeMaxTokens = 1024
)

// ClaudeAdapter 는 공식 Anthropic Go SDK 를 사용하는 Messages API(스트리밍, tool use) 어댑터다.
type ClaudeAdapter struct {
	endpoint   string
	model      string
	apiKey     string // 직접 저장된 키(복호화된 평문). 비면 apiKeyEnv 환경변수 사용
	apiKeyEnv  string
	httpClient *http.Client // 공유 커넥션 풀(factory 가 주입)
}

// NewClaudeAdapter 는 도메인 ProviderConfig 로부터 ClaudeAdapter 를 생성한다.
func NewClaudeAdapter(config domainllmprovider.ProviderConfig, httpClient *http.Client) *ClaudeAdapter {
	return &ClaudeAdapter{
		endpoint:   config.Endpoint,
		model:      config.Model,
		apiKey:     config.APIKey,
		apiKeyEnv:  config.APIKeyEnv,
		httpClient: httpClient,
	}
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *ClaudeAdapter) ProviderType() domainllmprovider.ProviderType {
	return domainllmprovider.ProviderTypeExternal
}

// resolveModel 은 사용할 모델을 결정한다.
// 모델은 서버(관리자)의 Provider 설정으로만 정해지며 클라이언트 요청은 무시한다.
func (a *ClaudeAdapter) resolveModel(_ string) string {
	return modelOrDefault(a.model, defaultClaudeModel)
}

// cacheBreakpoint 는 cache_control 브레이크포인트 값이다.
//
// Type 을 반드시 명시해야 한다. 빈 리터럴(CacheControlEphemeralParam{})은 모든 필드가 제로값이라
// SDK 의 `json:"cache_control,omitzero"` 태그에 걸려 직렬화에서 통째로 빠진다 — 즉 캐싱이
// 컴파일 오류도 런타임 오류도 없이 "조용한 no-op" 이 된다. 실측으로 확인했다:
//
//	CacheControlEphemeralParam{}                  → {"name":"x"}
//	CacheControlEphemeralParam{Type:"ephemeral"}  → {"name":"x","cache_control":{"type":"ephemeral"}}
//
// TTL 은 생략해 기본값(5분)을 쓴다. 에이전트 루프는 반복 간격이 수 초라 5분으로 충분하다.
func cacheBreakpoint() anthropic.CacheControlEphemeralParam {
	return anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
}

// claudeBuildSystem 은 system 역할 메시지들을 top-level System 텍스트 블록 배열로 분리한다.
// 여러 system 메시지는 "\n\n" 로 결합한 단일 텍스트 블록이 된다(없으면 nil).
//
// cacheHere=true 면 이 블록에 cache_control 을 건다. 도구가 없는 요청(예: 요약 전용 호출)에서만
// 사용한다 — 도구가 있으면 claudeBuildTools 의 브레이크포인트가 system 까지 함께 덮으므로 중복이다.
func claudeBuildSystem(msgs []domainllmprovider.ChatMessage, cacheHere bool) []anthropic.TextBlockParam {
	var systems []string
	for _, m := range msgs {
		if m.Role == domainllmprovider.ChatRoleSystem {
			systems = append(systems, m.Content)
		}
	}
	if len(systems) == 0 {
		return nil
	}
	block := anthropic.TextBlockParam{Text: strings.Join(systems, "\n\n")}
	if cacheHere {
		block.CacheControl = cacheBreakpoint()
	}
	return []anthropic.TextBlockParam{block}
}

// claudeBuildMessages 는 도메인 메시지를 Anthropic 메시지로 변환한다(멀티턴 히스토리).
// system 은 제외(top-level System), 연속 tool 결과는 하나의 user(tool_result)로, assistant+ToolCalls 는 tool_use 블록 배열로 재구성한다.
func claudeBuildMessages(msgs []domainllmprovider.ChatMessage) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))

	var pendingToolResults []anthropic.ContentBlockParamUnion
	flush := func() {
		if len(pendingToolResults) > 0 {
			out = append(out, anthropic.NewUserMessage(pendingToolResults...))
			pendingToolResults = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case domainllmprovider.ChatRoleSystem:
			continue
		case domainllmprovider.ChatRoleTool:
			// 연속된 tool 결과를 하나의 user 메시지로 모은다.
			pendingToolResults = append(pendingToolResults,
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false))
		case domainllmprovider.ChatRoleAssistant:
			flush()
			if len(m.ToolCalls) > 0 {
				blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+2)
				// thinking 블록은 반드시 "맨 앞"에 온다. Anthropic 은 thinking 이 켜진 상태에서
				// tool_use 가 있는 assistant 턴이면 그 앞에 thinking 블록(서명 포함)을 요구한다(없으면 400).
				if m.Thinking != "" && m.ThinkingSignature != "" {
					blocks = append(blocks, anthropic.ContentBlockParamUnion{
						OfThinking: &anthropic.ThinkingBlockParam{
							Thinking:  m.Thinking,
							Signature: m.ThinkingSignature,
						},
					})
				}
				if m.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(m.Content))
				}
				for _, tc := range m.ToolCalls {
					blocks = append(blocks, anthropic.ContentBlockParamUnion{
						OfToolUse: &anthropic.ToolUseBlockParam{
							ID:    tc.ID,
							Name:  tc.Name,
							Input: claudeDecodeArguments(tc.Arguments),
						},
					})
				}
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			} else {
				out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
			}
		default: // user
			flush()
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	flush()
	claudeMarkHistoryCache(out)
	return out
}

// claudeMarkHistoryCache 는 마지막 메시지의 마지막 블록에 cache_control 을 걸어 대화 프리픽스를 캐싱한다.
//
// 에이전트 루프는 매 반복마다 "지금까지의 전체 대화"를 다시 보낸다(서버는 stateless). 캐싱이 없으면
// 그 이력을 매번 정가로 재청구받으므로 비용이 반복 수에 대해 제곱으로 늘어난다. 매 요청의 끝을 표시해 두면
// 다음 반복에서 그 지점까지가 캐시 적중이 되고, 새로 늘어난 부분만 새로 청구된다.
//
// 브레이크포인트 예산: Anthropic 은 최대 4개를 허용한다. 여기서 1개, claudeBuildTools 에서 1개 → 총 2개.
func claudeMarkHistoryCache(msgs []anthropic.MessageParam) {
	if len(msgs) == 0 {
		return
	}
	blocks := msgs[len(msgs)-1].Content
	if len(blocks) == 0 {
		return
	}
	// GetCacheControl 은 활성 variant(OfText/OfToolResult/...)의 필드 포인터를 준다.
	// variant 가 cache_control 을 지원하지 않으면 nil 이며, 그 경우 조용히 넘어간다(캐싱은 최적화일 뿐).
	if cc := blocks[len(blocks)-1].GetCacheControl(); cc != nil {
		*cc = cacheBreakpoint()
	}
}

// claudeUsage 는 Anthropic usage 를 도메인 사용량으로 옮긴다.
//
// 핵심: Anthropic 은 캐시 적중분을 input_tokens 에서 "제외"하고 cache_read/cache_creation 으로 따로 보고한다.
// input_tokens 를 그대로 PromptTokens 에 넣으면 캐싱을 켜는 순간 쿼터 소모가 급감해 사용량 산정 기준이
// 조용히 바뀐다. 캐싱은 비용 최적화이지 정책 변경이 아니므로, PromptTokens 에는 "처리된 입력 총합"을 담아
// 캐싱 도입 전과 동일한 의미를 유지하고 캐시 내역은 별도 필드로 노출한다.
func claudeUsage(u anthropic.Usage) *domainllmprovider.ChatUsage {
	cacheRead := int(u.CacheReadInputTokens)
	cacheWrite := int(u.CacheCreationInputTokens)
	prompt := int(u.InputTokens) + cacheRead + cacheWrite
	completion := int(u.OutputTokens)

	return &domainllmprovider.ChatUsage{
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		TotalTokens:         prompt + completion,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheWrite,
	}
}

// claudeDecodeArguments 는 ToolCall.Arguments(JSON 문자열)를 tool_use input 으로 디코드한다.
// 비었거나 파싱 실패 시 빈 객체({})를 반환한다.
func claudeDecodeArguments(args string) any {
	if strings.TrimSpace(args) == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return map[string]any{}
	}
	return v
}

// claudeBuildTools 는 도메인 도구 정의를 Anthropic 도구로 변환한다.
// Parameters(raw JSON Schema)를 InputSchema 로 그대로 전달하며, 비면 {"type":"object"} 기본값을 쓴다.
//
// 마지막 도구에 cache_control 을 걸어 [system + tools] 프리픽스를 캐싱한다.
// 프롬프트 순서가 system → tools → messages 이고 캐시는 "표시된 블록까지의 프리픽스"를 잡으므로,
// 도구 끝에 한 번만 걸면 시스템 프롬프트까지 함께 캐시된다(브레이크포인트 1개 절약).
// 이 구간은 세션 내내 바뀌지 않는데 에이전트 루프는 매 반복 이를 전부 재전송하므로 적중률이 높다.
func claudeBuildTools(tools []domainllmprovider.ToolDefinition) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: claudeToolInputSchema(t.Parameters),
		}
		if t.Description != "" {
			tool.Description = anthropic.String(t.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	out[len(out)-1].OfTool.CacheControl = cacheBreakpoint()
	return out
}

// claudeToolInputSchema 는 raw JSON Schema(object) 바이트를 ToolInputSchemaParam 으로 만든다.
// 스키마의 properties/required/추가 키를 ExtraFields 로 보존한다(비면 type=object 만).
func claudeToolInputSchema(raw []byte) anthropic.ToolInputSchemaParam {
	schema := anthropic.ToolInputSchemaParam{}
	if len(raw) == 0 {
		return schema
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return schema
	}
	if props, ok := parsed["properties"]; ok {
		schema.Properties = props
	}
	if req, ok := parsed["required"].([]any); ok {
		required := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				required = append(required, s)
			}
		}
		schema.Required = required
	}
	// type/properties/required 외 추가 키(예: $defs, additionalProperties)는 ExtraFields 로 보존.
	extra := map[string]any{}
	for k, v := range parsed {
		switch k {
		case "type", "properties", "required":
			continue
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		schema.ExtraFields = extra
	}
	return schema
}

// ChatStream 은 Anthropic Messages API 를 스트리밍 호출해 텍스트 델타는 즉시 onChunk(Delta)로 보낸다.
// tool_use 블록은 누적된 최종 메시지에서 추출해 마지막 Done 조각에 담는다(onChunk 에러 시 중단).
func (a *ClaudeAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey, err := requireAPIKey("claude", a.apiKey, a.apiKeyEnv)
	if err != nil {
		return err
	}

	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if a.httpClient != nil {
		opts = append(opts, option.WithHTTPClient(a.httpClient))
	}
	if a.endpoint != "" {
		opts = append(opts, option.WithBaseURL(a.endpoint))
	}
	client := anthropic.NewClient(opts...)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClaudeMaxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.resolveModel(req.Model)),
		MaxTokens: int64(maxTokens),
		Messages:  claudeBuildMessages(req.Messages),
	}
	// 도구가 있으면 도구 끝의 브레이크포인트가 system 까지 덮으므로, system 자체 표시는 도구가 없을 때만.
	if system := claudeBuildSystem(req.Messages, len(req.Tools) == 0); len(system) > 0 {
		params.System = system
	}
	if tools := claudeBuildTools(req.Tools); len(tools) > 0 {
		params.Tools = tools
	}
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
	}
	if think := claudeThinking(req.Thinking); think != nil {
		params.Thinking = *think
	}

	stream := client.Messages.NewStreaming(ctx, params)
	// Next() 는 EOF 에도 응답 바디를 닫지 않는다 — Close 없이는 조기 반환(accumulate/onChunk 에러)마다 커넥션 누수.
	defer func() { _ = stream.Close() }()

	// 누적기: 스트림 이벤트로 최종 메시지(텍스트/사고/tool_use/stop_reason/usage)를 조립한다.
	message := anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return fmt.Errorf("claude: %w: accumulate stream event: %v", domainllmprovider.ErrUpstream, err)
		}
		// 델타는 도착 즉시 흘려보낸다. 텍스트와 사고는 서로 다른 델타 타입이라 분리해 전달한다.
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			switch d := delta.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if d.Text != "" {
					if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: d.Text}); err != nil {
						return err
					}
				}
			case anthropic.ThinkingDelta:
				if d.Thinking != "" {
					if err := onChunk(domainllmprovider.ChatStreamChunk{ThinkingDelta: d.Thinking}); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("claude: %w: %v", domainllmprovider.ErrUpstream, err)
	}

	// 최종 메시지에서 tool_use 와 thinking 블록을 추출한다.
	var toolCalls []domainllmprovider.ToolCall
	var thinkingText, thinkingSig string
	for _, block := range message.Content {
		switch b := block.AsAny().(type) {
		case anthropic.ToolUseBlock:
			toolCalls = append(toolCalls, domainllmprovider.ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: string(b.Input),
			})
		case anthropic.ThinkingBlock:
			// 서명은 다음 요청의 재생에 필요하다 — 바이트 그대로 보존한다.
			thinkingText = b.Thinking
			thinkingSig = b.Signature
		}
	}

	usage := claudeUsage(message.Usage)

	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:              true,
		FinishReason:      string(message.StopReason), // "end_turn" | "tool_use" | "max_tokens" 등 원문
		ToolCalls:         toolCalls,
		Usage:             usage,
		Thinking:          thinkingText,
		ThinkingSignature: thinkingSig,
	})
}

// claudeThinking 은 도메인 사고 설정을 SDK union 으로 옮긴다(nil 이면 아무것도 보내지 않음).
//
// 모델별 형식 차이를 그대로 전달만 한다(중계기 원칙) — 능력을 추측하지 않으므로 안 맞으면 Anthropic 이 400.
func claudeThinking(cfg *domainllmprovider.ThinkingConfig) *anthropic.ThinkingConfigParamUnion {
	if cfg == nil {
		return nil
	}
	switch cfg.Type {
	case "adaptive":
		u := anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		return &u
	case "enabled":
		u := anthropic.ThinkingConfigParamOfEnabled(int64(cfg.BudgetTokens))
		return &u
	default:
		return nil // 알 수 없는 타입은 무시(미사용과 동일) — 오타로 요청이 깨지지 않게.
	}
}
