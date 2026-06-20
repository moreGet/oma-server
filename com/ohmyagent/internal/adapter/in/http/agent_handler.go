package httpin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainagent "aiagent/com/ohmyagent/internal/domain/agent"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// AgentHandler 는 /api/v1/agent/chat (에이전트 루프 중계, SSE) 핸들러다.
type AgentHandler struct {
	svc domainagent.Service
}

// NewAgentHandler 는 AgentHandler 를 생성한다.
func NewAgentHandler(svc domainagent.Service) *AgentHandler {
	return &AgentHandler{svc: svc}
}

// Chat 은 POST /api/v1/agent/chat 를 처리한다.
//
// 응답: text/event-stream(SSE). named 이벤트:
//   - event: message_start  data: {"role":"assistant","model":"..."}
//   - event: content_delta  data: {"delta":"..."}
//   - event: tool_call      data: {"id":"...","name":"...","arguments":"{...}"}
//   - event: message_stop   data: {"stop_reason":"tool_use|end_turn|max_tokens","usage":{...}}
//   - event: error          data: {"error":{"code":"...","message":"..."}}  (스트리밍 중 오류)
//
// 스트리밍 시작 전 오류는 HandleAgent 가 중첩 JSON 에러로 직렬화한다.
func (h *AgentHandler) Chat(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())

	var req agentChatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	cmd := req.toCommand(claims.MemberID)

	rc := http.NewResponseController(w)
	wroteHeader := false
	ensureHeader := func() error {
		if wroteHeader {
			return nil
		}
		_ = rc.SetWriteDeadline(time.Time{}) // 스트리밍: write deadline 해제
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		wroteHeader = true
		if err := writeSSEEvent(w, "message_start", agentStartDTO{Role: "assistant", Model: cmd.Model}); err != nil {
			return err
		}
		return rc.Flush()
	}

	streamErr := h.svc.Stream(r.Context(), cmd, func(ev domainagent.Event) error {
		if err := ensureHeader(); err != nil {
			return err
		}
		if err := writeAgentEvent(w, ev); err != nil {
			return err
		}
		return rc.Flush()
	})

	if streamErr != nil {
		if !wroteHeader {
			return agentErrToHTTP(streamErr) // 시작 전 → HandleAgent 가 중첩 JSON 에러로
		}
		ae := toAppError(agentErrToHTTP(streamErr))
		_ = writeSSEEvent(w, "error", agentErrorBody{agentErrorDetail{Code: agentCode(ae), Message: ae.Message}})
		_ = rc.Flush()
		return nil
	}

	// 이벤트가 전혀 없던 경우에도 SSE 형태 유지(message_start 만이라도).
	if !wroteHeader {
		return ensureHeader()
	}
	return nil
}

// writeAgentEvent 는 도메인 이벤트를 named SSE 이벤트로 기록한다.
func writeAgentEvent(w http.ResponseWriter, ev domainagent.Event) error {
	switch ev.Kind {
	case domainagent.EventContentDelta:
		return writeSSEEvent(w, "content_delta", agentDeltaDTO{Delta: ev.Delta})
	case domainagent.EventToolCall:
		tc := ev.ToolCall
		return writeSSEEvent(w, "tool_call", agentToolCallDTO{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	case domainagent.EventMessageStop:
		dto := agentStopDTO{StopReason: ev.StopReason}
		if ev.Usage != nil {
			dto.Usage = &chatUsageDTO{
				PromptTokens:     ev.Usage.PromptTokens,
				CompletionTokens: ev.Usage.CompletionTokens,
				TotalTokens:      ev.Usage.TotalTokens,
			}
		}
		return writeSSEEvent(w, "message_stop", dto)
	default:
		return nil
	}
}

// writeSSEEvent 는 `event: <name>\ndata: {json}\n\n` 형식으로 기록한다.
func writeSSEEvent(w http.ResponseWriter, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

// ---------------------------------------------------------------------------
// DTO (snake_case)
// ---------------------------------------------------------------------------

type agentAttachmentDTO struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	DataBase64  string `json:"data_base64"`
}

type agentToolCallDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type agentMessageDTO struct {
	Role        string               `json:"role"`
	Content     string               `json:"content,omitempty"`
	ToolCallID  string               `json:"tool_call_id,omitempty"`
	ToolCalls   []agentToolCallDTO   `json:"tool_calls,omitempty"`
	Name        string               `json:"name,omitempty"`
	Attachments []agentAttachmentDTO `json:"attachments,omitempty"`
}

type agentToolDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type agentMetadataDTO struct {
	OS            string `json:"os,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type agentChatReq struct {
	Messages    []agentMessageDTO `json:"messages"`
	Tools       []agentToolDTO    `json:"tools,omitempty"`
	Model       string            `json:"model,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	Metadata    *agentMetadataDTO `json:"metadata,omitempty"`
	Stream      *bool             `json:"stream,omitempty"` // 수용하되 서버는 항상 SSE 스트리밍
}

func (req agentChatReq) toCommand(actorID string) domainagent.ChatCommand {
	msgs := make([]domainagent.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		dm := domainagent.Message{
			Role:       domainagent.Role(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			dm.ToolCalls = append(dm.ToolCalls, domainagent.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		for _, a := range m.Attachments {
			dm.Attachments = append(dm.Attachments, domainagent.Attachment{
				FileName: a.FileName, ContentType: a.ContentType, SizeBytes: a.SizeBytes, DataBase64: a.DataBase64,
			})
		}
		msgs = append(msgs, dm)
	}
	var tools []domainagent.ToolDefinition
	for _, t := range req.Tools {
		tools = append(tools, domainagent.ToolDefinition{Name: t.Name, Description: t.Description, Parameters: []byte(t.Parameters)})
	}
	cmd := domainagent.ChatCommand{
		Messages:    msgs,
		Tools:       tools,
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		ActorID:     actorID,
	}
	if req.Metadata != nil {
		cmd.Metadata = domainagent.Metadata{OS: req.Metadata.OS, WorkspaceRoot: req.Metadata.WorkspaceRoot}
	}
	return cmd
}

type agentStartDTO struct {
	Role  string `json:"role"`
	Model string `json:"model,omitempty"`
}

type agentDeltaDTO struct {
	Delta string `json:"delta"`
}

type agentStopDTO struct {
	StopReason string        `json:"stop_reason"`
	Usage      *chatUsageDTO `json:"usage,omitempty"`
}

// agentErrToHTTP 는 agent/llmprovider 도메인 에러를 AppError 로 매핑한다.
func agentErrToHTTP(err error) error {
	var ve *domainagent.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainllmprovider.ErrNoActiveProvider):
		return ErrNotFound("no active llm provider")
	case errors.Is(err, domainllmprovider.ErrChatUnsupported):
		return ErrBadGateway("active provider does not support chat")
	case errors.Is(err, domainllmprovider.ErrUpstream):
		return ErrBadGateway("llm provider request failed")
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("permission denied")
	default:
		return err
	}
}
