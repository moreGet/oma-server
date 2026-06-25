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
	APIKey      string         `json:"api_key,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
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
}

// ChatRequest 는 어댑터에 전달되는 채팅 질의다.
// Model 이 비면 Provider 설정의 Model 을 사용한다(어댑터가 결정).
type ChatRequest struct {
	Messages    []ChatMessage
	Tools       []ToolDefinition // function-calling 도구 스키마(비면 일반 채팅)
	Model       string           // 선택적 오버라이드("" = Provider 기본 모델)
	MaxTokens   int              // 0 = 미지정
	Temperature *float64         // nil = 미지정
}

// ChatUsage 는 토큰 사용량이다(제공자가 보고할 때만 채워짐).
type ChatUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ChatStreamChunk 는 스트리밍 응답의 한 조각이다.
// Done=true 이면 마지막 조각(FinishReason/Usage/ToolCalls 동반 가능)이다.
type ChatStreamChunk struct {
	Delta        string     // 증분 텍스트
	ToolCalls    []ToolCall // 완성된 도구 호출(주로 마지막 조각에 동반)
	FinishReason string     // 제공자 원문 완료 사유(stop/length/tool_calls/end_turn/tool_use 등)
	Done         bool       // 마지막 조각 여부
	Usage        *ChatUsage // 최종 조각에서 제공자가 보고한 사용량
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
}

// Validate 는 ProviderConfig 의 형식·보안 제약을 검증한다.
// APIKeyEnv 는 환경변수 "이름"만 허용한다(예: OPENAI_API_KEY). 키 값을 직접 넣으면
// 시크릿이 DB 에 저장·로그에 노출될 수 있으므로 거부한다. 빈 값은 허용(LOCAL 등 키 불필요).
func (c ProviderConfig) Validate() error {
	if c.APIKeyEnv != "" && !validEnvVarName(c.APIKeyEnv) {
		return &ErrValidation{Msg: "api_key_env must be an environment variable NAME (e.g. OPENAI_API_KEY), not the key value"}
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
