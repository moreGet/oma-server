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

// resolveModel 은 요청 모델 → 설정 모델 → 기본값 순으로 사용할 모델을 결정한다.
func (a *ClaudeAdapter) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	if a.model != "" {
		return a.model
	}
	return defaultClaudeModel
}

// claudeBuildSystem 은 system 역할 메시지들을 top-level System 텍스트 블록 배열로 분리한다.
// 여러 system 메시지는 "\n\n" 로 결합한 단일 텍스트 블록이 된다(없으면 nil).
func claudeBuildSystem(msgs []domainllmprovider.ChatMessage) []anthropic.TextBlockParam {
	var systems []string
	for _, m := range msgs {
		if m.Role == domainllmprovider.ChatRoleSystem {
			systems = append(systems, m.Content)
		}
	}
	if len(systems) == 0 {
		return nil
	}
	return []anthropic.TextBlockParam{{Text: strings.Join(systems, "\n\n")}}
}

// claudeBuildMessages 는 도메인 메시지를 Anthropic 메시지로 변환한다.
//   - system 역할은 제외(top-level System 으로 분리됨).
//   - tool 역할(결과)들은 연속 병합되어 하나의 user 메시지(tool_result 블록 배열)로,
//   - assistant + ToolCalls 는 (선택적 text + ) tool_use 블록 배열의 assistant 메시지로,
//   - 그 외 plain user/assistant 는 단순 텍스트 메시지로 재구성한다(멀티턴 루프 히스토리).
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
				blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
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
	return out
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

// ChatStream 은 Anthropic Messages API 를 스트리밍으로 호출하고 응답 조각을 onChunk 로 전달한다.
// 텍스트 델타는 즉시 onChunk(Delta) 로 보내고, tool_use 블록은 누적된 최종 메시지에서 추출해
// 마지막 Done 조각에 담는다. onChunk 가 에러를 반환하면 스트리밍을 중단하고 그 에러를 반환한다.
func (a *ClaudeAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	apiKey := resolveAPIKey(a.apiKey, a.apiKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("claude: %w: API key not set (set config api_key or api_key_env)", domainllmprovider.ErrUpstream)
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
	if system := claudeBuildSystem(req.Messages); len(system) > 0 {
		params.System = system
	}
	if tools := claudeBuildTools(req.Tools); len(tools) > 0 {
		params.Tools = tools
	}
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
	}

	stream := client.Messages.NewStreaming(ctx, params)

	// 누적기: 스트림 이벤트로 최종 메시지(텍스트/tool_use/stop_reason/usage)를 조립한다.
	message := anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return fmt.Errorf("claude: %w: accumulate stream event: %v", domainllmprovider.ErrUpstream, err)
		}
		// 텍스트 델타는 도착 즉시 클라이언트로 흘려보낸다.
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok && text.Text != "" {
				if err := onChunk(domainllmprovider.ChatStreamChunk{Delta: text.Text}); err != nil {
					return err
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("claude: %w: %v", domainllmprovider.ErrUpstream, err)
	}

	// 최종 메시지에서 tool_use 블록을 추출한다(input 은 누적된 JSON 원문).
	var toolCalls []domainllmprovider.ToolCall
	for _, block := range message.Content {
		if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			toolCalls = append(toolCalls, domainllmprovider.ToolCall{
				ID:        tu.ID,
				Name:      tu.Name,
				Arguments: string(tu.Input),
			})
		}
	}

	usage := &domainllmprovider.ChatUsage{
		PromptTokens:     int(message.Usage.InputTokens),
		CompletionTokens: int(message.Usage.OutputTokens),
		TotalTokens:      int(message.Usage.InputTokens + message.Usage.OutputTokens),
	}

	return onChunk(domainllmprovider.ChatStreamChunk{
		Done:         true,
		FinishReason: string(message.StopReason), // "end_turn" | "tool_use" | "max_tokens" 등 원문
		ToolCalls:    toolCalls,
		Usage:        usage,
	})
}
