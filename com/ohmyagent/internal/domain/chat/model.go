// Package chat 는 "클라이언트 질의 → 활성 LLM → 응답" 인터페이스의 도메인 모델·포트를 담는다.
// 도메인 규칙(스펙 §1): 외부 import 금지. stdlib(errors/strings)만 사용한다.
package chat

import "strings"

// ErrValidation 은 입력 검증 실패를 나타내는 typed 에러다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// Role 은 대화 메시지의 발화자 역할이다.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// 검증 한계값.
const (
	maxTemperature = 2.0
)

// Message 는 대화 한 턴이다.
type Message struct {
	Role    Role
	Content string
}

// ChatCommand 는 클라이언트 질의 입력(유스케이스 커맨드)이다.
type ChatCommand struct {
	Messages    []Message
	Model       string   // 선택적 모델 오버라이드("" = 활성 Provider 기본 모델)
	MaxTokens   int      // 0 = 미지정
	Temperature *float64 // nil = 미지정
	ActorID     string   // 호출자 멤버 ID
}

// Usage 는 토큰 사용량이다.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StreamChunk 는 스트리밍 응답의 한 조각이다(핸들러가 SSE 로 직렬화).
type StreamChunk struct {
	Delta        string
	FinishReason string
	Done         bool
	Usage        *Usage
}

// Normalize 는 메시지 내용·모델명의 공백을 정리한다.
func (c *ChatCommand) Normalize() {
	for i := range c.Messages {
		c.Messages[i].Content = strings.TrimSpace(c.Messages[i].Content)
	}
	c.Model = strings.TrimSpace(c.Model)
}

// Validate 는 질의 입력을 검증한다.
func (c *ChatCommand) Validate() error {
	c.Normalize()
	if len(c.Messages) == 0 {
		return &ErrValidation{Msg: "messages must not be empty"}
	}
	for _, m := range c.Messages {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			return &ErrValidation{Msg: "message role must be one of system|user|assistant"}
		}
		if m.Content == "" {
			return &ErrValidation{Msg: "message content must not be empty"}
		}
	}
	if c.MaxTokens < 0 {
		return &ErrValidation{Msg: "max_tokens must be >= 0"}
	}
	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > maxTemperature) {
		return &ErrValidation{Msg: "temperature must be between 0 and 2"}
	}
	return nil
}
