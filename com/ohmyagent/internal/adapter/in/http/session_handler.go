package httpin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainchatsession "aiagent/com/ohmyagent/internal/domain/chatsession"
)

// SessionHandler 는 /api/v1/agent/sessions (채팅 히스토리 서버 동기화) 핸들러다.
type SessionHandler struct {
	svc domainchatsession.Service
}

// NewSessionHandler 는 SessionHandler 를 생성한다.
func NewSessionHandler(svc domainchatsession.Service) *SessionHandler {
	return &SessionHandler{svc: svc}
}

// --- GET /api/v1/agent/sessions ---

func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	summaries, err := h.svc.List(r.Context(), claims.MemberID)
	if err != nil {
		return sessionErrToHTTP(err)
	}
	items := make([]sessionSummaryDTO, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, sessionSummaryDTO{ID: s.ID, Title: s.Title, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, sessionsListResp{Sessions: items})
	return nil
}

// --- GET /api/v1/agent/sessions/{id} ---

func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	s, err := h.svc.Get(r.Context(), claims.MemberID, r.PathValue("id"))
	if err != nil {
		return sessionErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toSessionResp(s))
	return nil
}

// --- PUT /api/v1/agent/sessions/{id} ---

func (h *SessionHandler) Upsert(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req upsertSessionReq
	if err := decodeJSON(w, r, maxLargeJSONBytes, &req); err != nil {
		return err
	}
	s, err := h.svc.Upsert(r.Context(), domainchatsession.UpsertCommand{
		ID:      r.PathValue("id"),
		OwnerID: claims.MemberID,
		Title:   req.Title,
		Data:    []byte(req.Data),
	})
	if err != nil {
		return sessionErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toSessionResp(s))
	return nil
}

// --- DELETE /api/v1/agent/sessions/{id} ---

func (h *SessionHandler) Delete(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	if err := h.svc.Delete(r.Context(), claims.MemberID, r.PathValue("id")); err != nil {
		return sessionErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type sessionSummaryDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type sessionsListResp struct {
	Sessions []sessionSummaryDTO `json:"sessions"`
}

type sessionResp struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type upsertSessionReq struct {
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

func toSessionResp(s domainchatsession.Session) sessionResp {
	data := json.RawMessage(s.Data)
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return sessionResp{ID: s.ID, Title: s.Title, Data: data, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

// sessionErrToHTTP 는 chatsession 도메인 에러를 AppError 로 매핑한다.
func sessionErrToHTTP(err error) error {
	var ve *domainchatsession.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainchatsession.ErrNotFound):
		return ErrNotFound("session not found")
	case errors.Is(err, domainchatsession.ErrPermission):
		return ErrForbidden("permission denied")
	default:
		return err
	}
}
