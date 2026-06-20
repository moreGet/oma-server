// Package agent 는 에이전트 루프(클라이언트 ↔ 백엔드 LLM function-calling 중계)의 도메인 모델·포트를 담는다.
// 서버는 도구를 실행하지 않는다: 도구 스키마를 LLM 에 전달하고, LLM 의 도구 호출 요청·텍스트를 스트리밍으로 중계한다.
// 도메인 규칙(스펙 §1): 외부 import 금지. stdlib(errors/strings)만 사용한다.
package agent

import "strings"

// ErrValidation 은 입력 검증 실패를 나타내는 typed 에러다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// Role 은 대화 메시지 역할이다.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// stop_reason 정규화 값(클라이언트 계약 어휘).
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// 검증 한계값.
const (
	MaxAttachmentBytes = 10 << 20 // 10 MiB / 파일
	maxTemperature     = 2.0
)

// Attachment 는 메시지에 첨부된 파일(inline base64)이다.
type Attachment struct {
	FileName    string
	ContentType string
	SizeBytes   int64
	DataBase64  string
}

// ToolCall 은 LLM 이 요청한(또는 히스토리상의) 도구 호출이다.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON 문자열
}

// ToolDefinition 은 클라이언트가 보낸 도구(함수) 스키마다. Parameters 는 JSON Schema 원문.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []byte
}

// Message 는 대화 한 턴이다.
type Message struct {
	Role        Role
	Content     string
	ToolCallID  string       // role=tool: 결과 대상 호출 ID
	ToolCalls   []ToolCall   // role=assistant: 모델이 만든 도구 호출(히스토리)
	Name        string       // 선택적 도구/함수 이름
	Attachments []Attachment // 첨부(요구 D)
}

// Metadata 는 클라이언트 환경 힌트다(os/workspace 등).
type Metadata struct {
	OS            string
	WorkspaceRoot string
}

// ChatCommand 는 에이전트 질의 입력이다.
type ChatCommand struct {
	Messages    []Message
	Tools       []ToolDefinition
	Model       string
	MaxTokens   int
	Temperature *float64
	Metadata    Metadata
	ActorID     string
}

// --- 출력 이벤트(유스케이스 → 핸들러 SSE) ---

// EventKind 는 스트리밍 이벤트 종류다.
type EventKind string

const (
	EventContentDelta EventKind = "content_delta"
	EventToolCall     EventKind = "tool_call"
	EventMessageStop  EventKind = "message_stop"
)

// Usage 는 토큰 사용량이다.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Event 는 스트리밍 출력 이벤트다.
type Event struct {
	Kind       EventKind
	Delta      string    // content_delta
	ToolCall   *ToolCall // tool_call
	StopReason string    // message_stop (end_turn/tool_use/max_tokens)
	Usage      *Usage    // message_stop
}

// Normalize 는 모델명·메시지 내용 공백을 정리한다.
func (c *ChatCommand) Normalize() {
	c.Model = strings.TrimSpace(c.Model)
	for i := range c.Messages {
		c.Messages[i].Content = strings.TrimSpace(c.Messages[i].Content)
	}
}

// Validate 는 에이전트 질의 입력을 검증한다.
func (c *ChatCommand) Validate() error {
	c.Normalize()
	if len(c.Messages) == 0 {
		return &ErrValidation{Msg: "messages must not be empty"}
	}
	for _, m := range c.Messages {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		default:
			return &ErrValidation{Msg: "message role must be one of system|user|assistant|tool"}
		}
		if m.Role == RoleTool && m.ToolCallID == "" {
			return &ErrValidation{Msg: "tool message requires tool_call_id"}
		}
		// content/tool_calls/attachments 가 모두 비면 의미 없는 메시지.
		if m.Content == "" && len(m.ToolCalls) == 0 && len(m.Attachments) == 0 {
			return &ErrValidation{Msg: "message must have content, tool_calls, or attachments"}
		}
		for _, a := range m.Attachments {
			if a.DataBase64 == "" {
				return &ErrValidation{Msg: "attachment data_base64 is required"}
			}
			if a.SizeBytes > MaxAttachmentBytes {
				return &ErrValidation{Msg: "attachment exceeds size limit (10MiB)"}
			}
			if !IsAllowedAttachment(a.ContentType) {
				return &ErrValidation{Msg: "unsupported attachment content_type: " + a.ContentType}
			}
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

// NormalizeStopReason 은 제공자별 finish_reason 을 클라이언트 어휘(end_turn/tool_use/max_tokens)로 정규화한다.
func NormalizeStopReason(raw string, hasToolCalls bool) string {
	switch raw {
	case "tool_calls", "tool_use":
		return StopToolUse
	case "length", "max_tokens":
		return StopMaxTokens
	}
	if hasToolCalls {
		return StopToolUse
	}
	return StopEndTurn
}

// IsAllowedAttachment 은 허용 MIME 인지 판정한다(text 계열·일부 application·image·pdf).
func IsAllowedAttachment(contentType string) bool {
	if IsTextLikeAttachment(contentType) {
		return true
	}
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return true
	case contentType == "application/pdf":
		return true
	default:
		return false
	}
}

// IsTextLikeAttachment 은 본문을 그대로 인라인해도 되는 텍스트 계열인지 판정한다.
func IsTextLikeAttachment(contentType string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript", "application/x-sh":
		return true
	default:
		return false
	}
}
