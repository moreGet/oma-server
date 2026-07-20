// Package llmprovider 는 LLM 제공자 어그리거트의 도메인 모델·포트를 담는다.
// 도메인 규칙(스펙 §1): 외부 import 금지. stdlib(errors/strings/time)만 사용한다.
package llmprovider

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러(핸들러가 HTTP 코드로 매핑) ---
var (
	ErrNotFound         = errors.New("llm provider not found")               // → 404
	ErrNoActiveProvider = errors.New("no active llm provider")               // → 404
	ErrConflict         = errors.New("provider already exists")              // → 409
	ErrUpstream         = errors.New("llm provider upstream error")          // → 502 (외부 LLM 호출 실패/비정상 응답)
	ErrChatUnsupported  = errors.New("chat not supported for this provider") // → 502 (어댑터가 채팅 미구현)
)

// ErrValidation 은 입력 검증 실패를 나타내는 typed 에러다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// ProviderType 은 LLM 제공자의 배포 유형이다(기존 타입 보존).
type ProviderType string

const (
	ProviderTypeLocal    ProviderType = "LOCAL"
	ProviderTypeExternal ProviderType = "EXTERNAL"
)

// ProviderConfig 는 config_json 컬럼에 저장되는 가변 설정의 도메인 표현이다(기존 JSON 태그 보존).
// 시크릿(API 키)은 직접 저장하지 않고, APIKeyEnv 에 환경변수 이름만 저장한다.
type ProviderConfig struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // 환경변수명만 저장(시크릿 아님)
	// APIKey 는 DB(config_json)에 **AES-GCM 암호문**으로 저장된다. 어댑터에 전달될 때만
	// 유스케이스가 복호화한 평문으로 채운다. 응답 DTO 에는 절대 노출하지 않는다(마스킹).
	APIKey    string `json:"api_key,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	// Reasoning 은 추론 강도(OpenAI reasoning_effort)다. 빈 값 = 미지정(파라미터 자체를 보내지 않음).
	//
	// 클라이언트는 이 값을 지정할 수 없다 — 서버(관리자)가 Provider 단위로 정하고 모든 요청이 그것을 따른다.
	// 확장 사고(ThinkingConfig)가 클라이언트 주도인 것과 반대 방향이며, 의도적이다.
	Reasoning string `json:"reasoning,omitempty"`
	// APIStyle 은 OpenAI 호출 방식이다. 빈 값/chat_completions = /v1/chat/completions(기본),
	// responses = /v1/responses.
	//
	// 자동 판별하지 않는 이유: /v1/chat/completions 만 구현한 OpenAI 호환 서버(vLLM·LiteLLM 등)가
	// 많아서, 서버가 임의로 /v1/responses 로 바꾸면 그런 엔드포인트가 조용히 깨진다.
	// 추론(reasoning)과 도구(function tools)를 동시에 쓰려면 responses 가 필요하다.
	APIStyle    string         `json:"api_style,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

// OpenAI 호출 방식.
const (
	APIStyleChatCompletions = "chat_completions"
	APIStyleResponses       = "responses"
)

// APIStyles 는 선택 가능한 호출 방식이다(빈 값 = chat_completions 기본).
var APIStyles = []string{APIStyleChatCompletions, APIStyleResponses}

// UsesResponsesAPI 는 /v1/responses 방식인지 반환한다.
func (c ProviderConfig) UsesResponsesAPI() bool {
	return c.APIStyle == APIStyleResponses
}

// ReasoningEfforts 는 허용되는 추론 강도 값이다(빈 값 = 미지정은 별도 허용).
//
// 관리자 오타가 벤더의 불투명한 400 으로만 드러나는 걸 막기 위한 방어선이다.
// 벤더가 새 값을 추가하면 이 슬라이스에 한 줄 추가하면 된다.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidReasoning 은 추론 강도 값이 허용 목록에 있는지 검사한다(빈 값 = 미지정도 유효).
func ValidReasoning(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range ReasoningEfforts {
		if s == v {
			return true
		}
	}
	return false
}

// LLMProvider 는 도메인 애그리거트 루트.
// 변경점(기존 대비): ID int64 → string(UUID v4), 감사필드 추가.
type LLMProvider struct {
	ID           string // UUID v4
	Name         string
	IsActive     bool
	ProviderType ProviderType
	Config       ProviderConfig
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CreatedBy    string
	UpdatedBy    string
}

// --- 채팅 I/O (Adapter out 포트가 사용하는 타입) ---

// ChatRole 은 대화 메시지의 발화자 역할이다.
type ChatRole string

const (
	ChatRoleSystem    ChatRole = "system"
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
	ChatRoleTool      ChatRole = "tool" // 도구 실행 결과 메시지(멀티턴 에이전트 루프)
)

// ToolDefinition 은 LLM function-calling 에 전달하는 도구(함수) 스키마다.
// Parameters 는 JSON Schema(object) 원문 바이트다(도메인은 JSON 비의존, 어댑터가 그대로 전달).
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []byte // raw JSON Schema
}

// ToolCall 은 LLM 이 요청한 도구 호출이다. Arguments 는 JSON 문자열.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatMessage 는 대화 한 턴이다.
//   - role=assistant + ToolCalls: 모델이 이전 턴에 요청한 도구 호출(히스토리 재생용)
//   - role=tool + ToolCallID/Content: 그 도구의 실행 결과
type ChatMessage struct {
	Role       ChatRole
	Content    string
	ToolCallID string     // role=tool: 어떤 호출에 대한 결과인지
	ToolCalls  []ToolCall // role=assistant: 모델이 만든 도구 호출
	Name       string     // 선택적 도구/함수 이름

	// 확장 사고(extended thinking) 재생용. Anthropic 은 thinking 이 켜진 상태에서 도구를 쓰면
	// 그 턴의 assistant 메시지에 thinking 블록(서명 포함)을 "반드시" 되돌려 보내라고 요구한다.
	// 빠지면 400 이다. 클라이언트가 직전 assistant 턴의 thinking 원문·서명을 여기 실어 보내면
	// 어댑터가 tool_use 앞에 thinking 블록으로 복원한다. thinking 미사용 시 둘 다 빈 값.
	Thinking          string // 모델이 생성한 사고 원문(assistant 턴)
	ThinkingSignature string // 그 사고 블록의 서명(변조 검증용, 바이트 그대로 보존해야 함)
}

// ThinkingConfig 는 확장 사고 설정이다(nil = 미사용, 어댑터가 아무것도 보내지 않음).
//
// 모델별로 받는 형식이 다르다는 점이 핵심이다:
//   - Type="adaptive": Opus 4.x/Sonnet 5/Fable 5 등 최신 모델(budget_tokens 는 이들에서 거부됨).
//   - Type="enabled" + BudgetTokens: Claude 3.7/구형 사고 모델.
// 서버는 모델 능력을 추측하지 않는다(중계기 원칙) — 클라이언트가 자기 모델에 맞는 형식을 지정하고,
// 안 맞으면 Anthropic 이 400 을 돌려주며 그대로 사용자에게 표면화된다.
type ThinkingConfig struct {
	Type         string // "adaptive" | "enabled"
	BudgetTokens int    // Type="enabled" 일 때만 사용(>=1024, < max_tokens)
}

// ChatRequest 는 어댑터에 전달되는 채팅 질의다.
// Model 이 비면 Provider 설정의 Model 을 사용한다(어댑터가 결정).
type ChatRequest struct {
	Messages    []ChatMessage
	Tools       []ToolDefinition // function-calling 도구 스키마(비면 일반 채팅)
	Model       string           // 선택적 오버라이드("" = Provider 기본 모델)
	MaxTokens   int              // 0 = 미지정
	Temperature *float64         // nil = 미지정
	Thinking    *ThinkingConfig  // nil = 확장 사고 미사용(기본)
}

// ChatUsage 는 토큰 사용량이다(제공자가 보고할 때만 채워짐).
//
// 프롬프트 캐싱 주의: Anthropic 은 캐시 적중분을 input_tokens 에서 "제외"하고 별도로 보고한다.
// 그대로 PromptTokens 에 넣으면 캐싱을 켜는 순간 사용량 집계가 급감해 쿼터 의미가 조용히 바뀐다.
// 그래서 PromptTokens 는 "처리된 입력 토큰 총합"(신규 + 캐시 읽기 + 캐시 생성)을 유지하고,
// 캐시 내역은 아래 두 필드로 따로 노출한다. 캐싱은 비용을 줄이는 것이지 사용량 산정 기준을 바꾸는 게 아니다.
type ChatUsage struct {
	PromptTokens     int // 처리된 입력 토큰 총합(캐시 적중분 포함) — 쿼터 산정 기준
	CompletionTokens int
	TotalTokens      int

	// CacheReadTokens 는 캐시에서 읽은 입력 토큰이다(정가의 약 10%로 청구). 0 이면 캐시 미적중.
	CacheReadTokens int
	// CacheCreationTokens 는 캐시에 쓴 입력 토큰이다(정가의 약 125%로 청구). 첫 요청에서만 발생.
	CacheCreationTokens int
}

// ChatStreamChunk 는 스트리밍 응답의 한 조각이다.
// Done=true 이면 마지막 조각(FinishReason/Usage/ToolCalls 동반 가능)이다.
type ChatStreamChunk struct {
	Delta         string     // 증분 텍스트
	ThinkingDelta string     // 증분 사고 텍스트(확장 사고 켜졌을 때만). Delta 와 상호배타적으로 채워진다.
	ToolCalls     []ToolCall // 완성된 도구 호출(주로 마지막 조각에 동반)
	FinishReason  string     // 제공자 원문 완료 사유(stop/length/tool_calls/end_turn/tool_use 등)
	Done          bool       // 마지막 조각 여부
	Usage         *ChatUsage // 최종 조각에서 제공자가 보고한 사용량

	// 최종 조각에서만 채워진다 — 이번 assistant 턴이 생성한 사고 블록의 원문·서명.
	// 클라이언트가 이력에 저장했다가 다음 요청에 되돌려 보내야 도구 사용 시 400 을 피한다.
	Thinking          string
	ThinkingSignature string
}

// --- 커맨드 ---

// CreateCommand 는 Provider 생성 입력이다.
type CreateCommand struct {
	Name         string
	ProviderType ProviderType
	IsActive     bool
	Config       ProviderConfig
	ActorID      string
}

// UpdateConfigCommand 는 Provider 설정 갱신 입력이다.
type UpdateConfigCommand struct {
	ID      string
	Config  ProviderConfig
	ActorID string
}

// ActivateCommand 는 Provider 활성화 입력이다.
type ActivateCommand struct {
	ID      string
	ActorID string
}

// DeleteCommand 는 Provider 삭제 입력이다.
type DeleteCommand struct {
	ID      string
	ActorID string
}

// Normalize 는 입력을 정규화한다.
func (c *CreateCommand) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Config.Normalize()
}

// Validate 는 Provider 생성 입력을 검증한다.
func (c *CreateCommand) Validate() error {
	c.Normalize()
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	if c.ProviderType != ProviderTypeLocal && c.ProviderType != ProviderTypeExternal {
		return &ErrValidation{Msg: "provider_type must be LOCAL or EXTERNAL"}
	}
	return c.Config.Validate()
}

// Validate 는 Provider 설정 갱신 입력을 검증한다.
func (c *UpdateConfigCommand) Validate() error {
	c.Config.Normalize()
	return c.Config.Validate()
}

// Normalize 는 ProviderConfig 입력의 공백을 제거한다.
func (c *ProviderConfig) Normalize() {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Model = strings.TrimSpace(c.Model)
	c.APIKeyEnv = strings.TrimSpace(c.APIKeyEnv)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Reasoning = strings.ToLower(strings.TrimSpace(c.Reasoning))
	c.APIStyle = strings.ToLower(strings.TrimSpace(c.APIStyle))
}

// Validate 는 ProviderConfig 의 형식·보안 제약을 검증한다.
// APIKeyEnv 는 환경변수 "이름"만 허용한다(예: OPENAI_API_KEY). 키 값을 직접 넣으면
// 시크릿이 DB 에 저장·로그에 노출될 수 있으므로 거부한다. 빈 값은 허용(LOCAL 등 키 불필요).
func (c ProviderConfig) Validate() error {
	if c.APIKeyEnv != "" && !validEnvVarName(c.APIKeyEnv) {
		return &ErrValidation{Msg: "api_key_env must be an environment variable NAME (e.g. OPENAI_API_KEY), not the key value"}
	}
	if !ValidReasoning(c.Reasoning) {
		return &ErrValidation{Msg: "reasoning must be one of " + strings.Join(ReasoningEfforts, ", ") + " (or empty)"}
	}
	if c.APIStyle != "" && c.APIStyle != APIStyleChatCompletions && c.APIStyle != APIStyleResponses {
		return &ErrValidation{Msg: "api_style must be one of " + strings.Join(APIStyles, ", ") + " (or empty)"}
	}
	return nil
}

// validEnvVarName 은 POSIX 환경변수 이름 규칙(^[A-Za-z_][A-Za-z0-9_]*$)을 만족하는지 검사한다.
// API 키 값(sk-..., 하이픈/소문자 혼합 등)은 이 규칙을 통과하지 못한다.
func validEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
