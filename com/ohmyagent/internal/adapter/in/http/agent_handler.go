package httpin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainagent "aiagent/com/ohmyagent/internal/domain/agent"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// AgentHandler 는 /api/v1/agent/chat (에이전트 루프 중계, SSE) 핸들러다.
type AgentHandler struct {
	svc        domainagent.Service
	recorder   domaintranscript.Recorder // 대화 이력 비차단 기록(nil 허용)
	stripAttch func() bool               // 이력 기록 시 첨부 본문 제거 여부(nil 허용)
	quota      quotaChecker              // 토큰 쿼터 시행(nil 허용)
}

// NewAgentHandler 는 AgentHandler 를 생성한다.
func NewAgentHandler(svc domainagent.Service, recorder domaintranscript.Recorder, stripAttachments func() bool, quota quotaChecker) *AgentHandler {
	return &AgentHandler{svc: svc, recorder: recorder, stripAttch: stripAttachments, quota: quota}
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
	if err := decodeJSON(w, r, maxLargeJSONBytes, &req); err != nil {
		return err
	}
	cmd := req.toCommand(claims.MemberID)

	// 쿼터 사전 검사: 이번 달 한도 초과면 스트리밍 시작 전 429.
	if h.quota != nil {
		if err := h.quota.Check(r.Context(), claims.MemberID); err != nil {
			return agentErrToHTTP(err)
		}
	}

	rc := http.NewResponseController(w)
	wroteHeader := false
	ensureHeader := func() error {
		if wroteHeader {
			return nil
		}
		writeSSEHeaders(w, rc)
		wroteHeader = true
		if err := writeSSEEvent(w, "message_start", agentStartDTO{Role: "assistant", Model: cmd.Model}); err != nil {
			return err
		}
		return rc.Flush()
	}

	start := time.Now()
	var respBuf strings.Builder
	var stopReason string
	var usage *domainagent.Usage

	streamErr := h.svc.Stream(r.Context(), cmd, func(ev domainagent.Event) error {
		if err := ensureHeader(); err != nil {
			return err
		}
		switch ev.Kind {
		case domainagent.EventContentDelta:
			respBuf.WriteString(ev.Delta)
		case domainagent.EventMessageStop:
			stopReason = ev.StopReason
			usage = ev.Usage
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

	// 정상 완료 시: 감사 이벤트 + 대화 이력 비동기 기록 + 쿼터 사용량 누적(usage 없으면 추정치).
	response := respBuf.String()
	h.recordAgent(claims.MemberID, req, response, stopReason, usage, start)
	if h.quota != nil {
		total := 0
		if usage != nil {
			total = usage.TotalTokens
		}
		ctx, cancel := accountingCtx(r)
		h.quota.Add(ctx, claims.MemberID, quotaTokens(total, func() string { return req.promptText() + response }))
		cancel()
	}

	// 이벤트가 전혀 없던 경우에도 SSE 형태 유지(message_start 만이라도).
	if !wroteHeader {
		return ensureHeader()
	}
	return nil
}

// recordAgent 은 agent.request 감사 이벤트(메타데이터)를 남기고 대화 이력을 비동기 기록한다.
func (h *AgentHandler) recordAgent(memberID string, req agentChatReq, response, stopReason string, usage *domainagent.Usage, start time.Time) {
	var pt, ct, tt int
	if usage != nil {
		pt, ct, tt = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
	}
	slog.Info("agent request", "event", "agent.request",
		"actor", memberID, "model", req.Model, "tools", len(req.Tools), "prompt_tokens", pt, "completion_tokens", ct,
		"stop_reason", stopReason, "latency_ms", time.Since(start).Milliseconds())
	if h.recorder == nil {
		return
	}
	if h.stripAttch != nil && h.stripAttch() {
		req = stripAttachmentData(req)
	}
	reqJSON, _ := json.Marshal(req)
	h.recorder.Record(domaintranscript.Transcript{
		MemberID:         memberID,
		Source:           domaintranscript.SourceAgent,
		Model:            req.Model,
		Request:          reqJSON,
		Response:         response,
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
		FinishReason:     stopReason,
	})
}

// stripAttachmentData 는 이력 저장용으로 첨부 본문(base64)을 제거한 사본을 만든다(메타데이터 보존).
func stripAttachmentData(req agentChatReq) agentChatReq {
	out := req
	out.Messages = make([]agentMessageDTO, len(req.Messages))
	for i, m := range req.Messages {
		out.Messages[i] = m
		if len(m.Attachments) == 0 {
			continue
		}
		atts := make([]agentAttachmentDTO, len(m.Attachments))
		for j, a := range m.Attachments {
			a.DataBase64 = ""
			atts[j] = a
		}
		out.Messages[i].Attachments = atts
	}
	return out
}

// writeAgentEvent 는 도메인 이벤트를 named SSE 이벤트로 기록한다.
func writeAgentEvent(w http.ResponseWriter, ev domainagent.Event) error {
	switch ev.Kind {
	case domainagent.EventContentDelta:
		return writeSSEEvent(w, "content_delta", agentDeltaDTO{Delta: ev.Delta})
	case domainagent.EventThinkingDelta:
		return writeSSEEvent(w, "thinking_delta", agentDeltaDTO{Delta: ev.Delta})
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

// DTO (snake_case)

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
	Role              string               `json:"role"`
	Content           string               `json:"content,omitempty"`
	ToolCallID        string               `json:"tool_call_id,omitempty"`
	ToolCalls         []agentToolCallDTO   `json:"tool_calls,omitempty"`
	Name              string               `json:"name,omitempty"`
	Attachments       []agentAttachmentDTO `json:"attachments,omitempty"`
	Thinking          string               `json:"thinking,omitempty"`           // 확장 사고 재생용(assistant)
	ThinkingSignature string               `json:"thinking_signature,omitempty"` // 그 사고 블록의 서명
}

type agentThinkingDTO struct {
	Type         string `json:"type"`                    // "adaptive" | "enabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // Type="enabled" 일 때만
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
	Thinking    *agentThinkingDTO `json:"thinking,omitempty"` // nil = 확장 사고 미사용(기본)
	Metadata    *agentMetadataDTO `json:"metadata,omitempty"`
	Stream      *bool             `json:"stream,omitempty"` // 수용하되 서버는 항상 SSE 스트리밍
}

// promptText 는 usage 추정용으로 메시지 본문을 이어붙인다(첨부 본문 제외).
func (req agentChatReq) promptText() string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func (req agentChatReq) toCommand(actorID string) domainagent.ChatCommand {
	msgs := make([]domainagent.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		dm := domainagent.Message{
			Role:              domainagent.Role(m.Role),
			Content:           m.Content,
			ToolCallID:        m.ToolCallID,
			Name:              m.Name,
			Thinking:          m.Thinking,
			ThinkingSignature: m.ThinkingSignature,
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
	if req.Thinking != nil {
		cmd.Thinking = &domainagent.ThinkingConfig{Type: req.Thinking.Type, BudgetTokens: req.Thinking.BudgetTokens}
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
	if errors.As(err, &ve) {
		return ErrBadRequest(ve.Msg)
	}
	if mapped := mapProviderQuotaAuthErr(err); mapped != nil {
		return mapped
	}
	return err
}

// mapProviderQuotaAuthErr 은 chat/agent 공통의 쿼터·LLM Provider·인가 에러를 HTTP 에러로 매핑한다.
// 매핑 대상이 아니면 nil 을 반환한다(호출부가 도메인별 검증 에러를 먼저 처리하고, 여기로 위임).
func mapProviderQuotaAuthErr(err error) error {
	var qe *domainquota.ExceededError
	switch {
	case errors.As(err, &qe):
		return ErrTooManyRequests(qe.Error()) // 윈도우·used/limit·리셋 시각 포함
	case errors.Is(err, domainquota.ErrExceeded):
		return ErrTooManyRequests("token quota exceeded")
	case errors.Is(err, domainllmprovider.ErrNoActiveProvider):
		return ErrNotFound("no active llm provider")
	case errors.Is(err, domainllmprovider.ErrChatUnsupported):
		return ErrBadGateway("active provider does not support chat")
	case errors.Is(err, domainllmprovider.ErrUpstream):
		return ErrBadGateway("llm provider request failed")
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("permission denied")
	default:
		return nil
	}
}
