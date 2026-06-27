package httpin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainchat "aiagent/com/ohmyagent/internal/domain/chat"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// quotaChecker 는 토큰 쿼터 시행 인터페이스다(*quotaapp.Service 가 충족, nil 허용).
type quotaChecker interface {
	Check(ctx context.Context, memberID string) error
	Add(ctx context.Context, memberID string, tokens int)
}

// quotaTokens 는 실측 total(>0)을 우선하고, usage 미제공(0) 시에만 estimate() 텍스트로 추정한다.
// estimate 는 지연 호출이라, usage 를 주는 정상 경로(대다수)에서 대화 전체를 잇는 큰 문자열 생성을 피한다.
func quotaTokens(total int, estimate func() string) int {
	if total > 0 {
		return total
	}
	return domainquota.EstimateTokens(estimate())
}

// ChatHandler 는 /api/v1/chat 질의(클라이언트 → 활성 LLM → SSE 응답) 핸들러다.
type ChatHandler struct {
	svc      domainchat.Service
	recorder domaintranscript.Recorder // 대화 이력 비차단 기록(nil 허용)
	quota    quotaChecker              // 토큰 쿼터 시행(nil 허용)
}

// NewChatHandler 는 ChatHandler 를 생성한다.
func NewChatHandler(svc domainchat.Service, recorder domaintranscript.Recorder, quota quotaChecker) *ChatHandler {
	return &ChatHandler{svc: svc, recorder: recorder, quota: quota}
}

// Stream 은 POST /api/v1/chat 를 처리한다.
//
// 응답: text/event-stream(SSE). 각 이벤트는 `data: {json}\n\n` 형식.
//   - 증분: {"delta":"...","done":false}
//   - 종료: {"done":true,"finish_reason":"stop","usage":{...}}
//   - 오류(스트리밍 중): {"error":"...","done":true}
//
// 스트리밍 시작(200 + SSE 헤더) 전에 발생한 에러(검증/활성 Provider 없음 등)는
// 일반 JSON AppError 로 반환한다. 시작 이후의 에러는 SSE error 이벤트로 보낸다.
func (h *ChatHandler) Stream(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())

	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}

	// 쿼터 사전 검사: 이번 달 한도 초과면 스트리밍 시작 전 429.
	if h.quota != nil {
		if err := h.quota.Check(r.Context(), claims.MemberID); err != nil {
			return chatErrToHTTP(err)
		}
	}

	rc := http.NewResponseController(w)
	wroteHeader := false
	// ensureHeader 는 첫 조각 직전에 SSE 헤더+200 을 1회 기록한다(지연 기록).
	ensureHeader := func() error {
		if wroteHeader {
			return nil
		}
		// 스트리밍은 장시간일 수 있으므로 write deadline 을 해제(서버 WriteTimeout 우회).
		_ = rc.SetWriteDeadline(time.Time{})
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // nginx 버퍼링 비활성
		w.WriteHeader(http.StatusOK)
		wroteHeader = true
		return rc.Flush()
	}

	start := time.Now()
	var respBuf strings.Builder
	var finishReason string
	var usage *domainchat.Usage

	streamErr := h.svc.Stream(r.Context(), req.toCommand(claims.MemberID), func(chunk domainchat.StreamChunk) error {
		if err := ensureHeader(); err != nil {
			return err
		}
		if chunk.Delta != "" {
			respBuf.WriteString(chunk.Delta)
		}
		if chunk.Done {
			finishReason = chunk.FinishReason
			usage = chunk.Usage
		}
		if err := writeSSE(w, toChatChunkDTO(chunk)); err != nil {
			return err
		}
		return rc.Flush()
	})

	if streamErr != nil {
		if !wroteHeader {
			// 아직 아무 것도 안 보냈으면 일반 HTTP 에러로 매핑(404/400/502 등).
			return chatErrToHTTP(streamErr)
		}
		// 이미 스트리밍 중이면 상태코드를 바꿀 수 없으므로 error 이벤트로 통지.
		_ = writeSSE(w, chatChunkDTO{Error: streamErr.Error(), Done: true})
		_ = rc.Flush()
		return nil
	}

	// 정상 완료 시: 감사 이벤트 + 대화 이력 비동기 기록 + 쿼터 사용량 누적(usage 없으면 추정치).
	response := respBuf.String()
	h.recordChat(claims.MemberID, req, response, finishReason, usage, start)
	if h.quota != nil {
		total := 0
		if usage != nil {
			total = usage.TotalTokens
		}
		h.quota.Add(r.Context(), claims.MemberID, quotaTokens(total, func() string { return req.promptText() + response }))
	}

	// 조각이 하나도 없던 경우에도 SSE 응답 형태를 유지한다.
	if !wroteHeader {
		if err := ensureHeader(); err != nil {
			return err
		}
		_ = writeSSE(w, chatChunkDTO{Done: true})
		_ = rc.Flush()
	}
	return nil
}

// recordChat 은 chat.request 감사 이벤트(메타데이터, 본문 없음)를 남기고
// 대화 이력(요청+응답)을 비동기 recorder 로 기록한다.
func (h *ChatHandler) recordChat(memberID string, req chatReq, response, finishReason string, usage *domainchat.Usage, start time.Time) {
	var pt, ct, tt int
	if usage != nil {
		pt, ct, tt = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
	}
	slog.Info("chat request", "event", "chat.request",
		"actor", memberID, "model", req.Model, "prompt_tokens", pt, "completion_tokens", ct,
		"finish_reason", finishReason, "latency_ms", time.Since(start).Milliseconds())
	if h.recorder == nil {
		return
	}
	reqJSON, _ := json.Marshal(req)
	h.recorder.Record(domaintranscript.Transcript{
		MemberID:         memberID,
		Source:           domaintranscript.SourceChat,
		Model:            req.Model,
		Request:          reqJSON,
		Response:         response,
		PromptTokens:     pt,
		CompletionTokens: ct,
		TotalTokens:      tt,
		FinishReason:     finishReason,
	})
}

// writeSSE 는 payload 를 `data: {json}\n\n` 형식으로 기록한다.
func writeSSE(w http.ResponseWriter, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// ---------------------------------------------------------------------------
// DTO (snake_case)
// ---------------------------------------------------------------------------

type chatMessageDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Messages    []chatMessageDTO `json:"messages"`
	Model       string           `json:"model,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
}

// promptText 는 usage 추정용으로 메시지 본문을 이어붙인다.
func (req chatReq) promptText() string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func (req chatReq) toCommand(actorID string) domainchat.ChatCommand {
	msgs := make([]domainchat.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, domainchat.Message{Role: domainchat.Role(m.Role), Content: m.Content})
	}
	return domainchat.ChatCommand{
		Messages:    msgs,
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		ActorID:     actorID,
	}
}

type chatUsageDTO struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatChunkDTO struct {
	Delta        string        `json:"delta,omitempty"`
	Done         bool          `json:"done"`
	FinishReason string        `json:"finish_reason,omitempty"`
	Usage        *chatUsageDTO `json:"usage,omitempty"`
	Error        string        `json:"error,omitempty"`
}

func toChatChunkDTO(c domainchat.StreamChunk) chatChunkDTO {
	dto := chatChunkDTO{Delta: c.Delta, Done: c.Done, FinishReason: c.FinishReason}
	if c.Usage != nil {
		dto.Usage = &chatUsageDTO{
			PromptTokens:     c.Usage.PromptTokens,
			CompletionTokens: c.Usage.CompletionTokens,
			TotalTokens:      c.Usage.TotalTokens,
		}
	}
	return dto
}

// chatErrToHTTP 는 chat/llmprovider 도메인 에러를 AppError 로 매핑한다.
func chatErrToHTTP(err error) error {
	var ve *domainchat.ErrValidation
	var qe *domainquota.ExceededError
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
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
		return err
	}
}
