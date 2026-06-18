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
	Endpoint    string         `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	APIKeyEnv   string         `json:"api_key_env,omitempty"` // 환경변수명만 저장(보안)
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
)

// ChatMessage 는 대화 한 턴이다.
type ChatMessage struct {
	Role    ChatRole
	Content string
}

// ChatRequest 는 어댑터에 전달되는 채팅 질의다.
// Model 이 비면 Provider 설정의 Model 을 사용한다(어댑터가 결정).
type ChatRequest struct {
	Messages    []ChatMessage
	Model       string   // 선택적 오버라이드("" = Provider 기본 모델)
	MaxTokens   int      // 0 = 미지정
	Temperature *float64 // nil = 미지정
}

// ChatUsage 는 토큰 사용량이다(제공자가 보고할 때만 채워짐).
type ChatUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ChatStreamChunk 는 스트리밍 응답의 한 조각이다.
// Done=true 이면 마지막 조각(FinishReason/Usage 동반 가능)이다.
type ChatStreamChunk struct {
	Delta        string     // 증분 텍스트
	FinishReason string     // 완료 사유(stop/length 등)
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
func (c *CreateCommand) Normalize() { c.Name = strings.TrimSpace(c.Name) }

// Validate 는 Provider 생성 입력을 검증한다.
func (c *CreateCommand) Validate() error {
	c.Normalize()
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	if c.ProviderType != ProviderTypeLocal && c.ProviderType != ProviderTypeExternal {
		return &ErrValidation{Msg: "provider_type must be LOCAL or EXTERNAL"}
	}
	return nil
}
