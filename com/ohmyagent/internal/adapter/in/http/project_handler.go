package httpin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

// projectService 는 프로젝트/대화 동기화 유스케이스 포트다(*projectapp.Service 가 충족).
type projectService interface {
	UpsertProject(ctx context.Context, cmd domainproject.UpsertProjectCommand) (domainproject.Project, error)
	ListProjects(ctx context.Context, ownerID string) ([]domainproject.Project, error)
	GetProject(ctx context.Context, ownerID, id string) (domainproject.Project, []domainproject.Conversation, error)
	DeleteProject(ctx context.Context, ownerID, id string) error
	UpsertConversation(ctx context.Context, cmd domainproject.UpsertConversationCommand) (domainproject.Conversation, error)
	DeleteConversation(ctx context.Context, ownerID, id string) error
}

// ProjectHandler 는 /api/v1/projects 동기화 핸들러다(중첩 에러 envelope).
type ProjectHandler struct{ svc projectService }

func NewProjectHandler(svc projectService) *ProjectHandler { return &ProjectHandler{svc: svc} }

// --- GET /api/v1/projects ---

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) error {
	owner := actorID(r)
	projects, err := h.svc.ListProjects(r.Context(), owner)
	if err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, projectsResp{Projects: mapSlice(projects, toProjectDTO)})
	return nil
}

// --- POST /api/v1/projects (생성/업서트) ---

func (h *ProjectHandler) Upsert(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[createProjectReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	p, err := h.svc.UpsertProject(r.Context(), domainproject.UpsertProjectCommand{
		OwnerID: actorID(r), ClientID: req.ClientID, Name: req.Name,
	})
	if err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, toProjectDTO(p))
	return nil
}

// --- GET /api/v1/projects/{id} (대화 요약 포함) ---

func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) error {
	p, convs, err := h.svc.GetProject(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, projectDetailDTO{
		ID: p.ID, Name: p.Name, Conversations: mapSlice(convs, toConversationSummaryDTO),
	})
	return nil
}

// --- DELETE /api/v1/projects/{id} ---

func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.DeleteProject(r.Context(), actorID(r), r.PathValue("id")); err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// --- POST /api/v1/projects/{id}/conversations (대화 업서트 push) ---

func (h *ProjectHandler) UpsertConversation(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req upsertConversationReq
	if err := decodeJSON(w, r, maxLargeJSONBytes, &req); err != nil {
		return err
	}
	c, err := h.svc.UpsertConversation(r.Context(), domainproject.UpsertConversationCommand{
		OwnerID:      actorID(r),
		ProjectID:    r.PathValue("id"),
		ClientID:     req.ClientID,
		Title:        req.Title,
		CreatedUTC:   parseUTC(req.CreatedUTC),
		UpdatedUTC:   parseUTC(req.UpdatedUTC),
		Messages:     normalizeMessages(req.Messages),
		MessageCount: messageCount(req.Messages),
	})
	if err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, conversationUpsertedDTO{ID: c.ID, ClientID: c.ClientID, UpdatedUTC: fmtUTC(c.UpdatedUTC)})
	return nil
}

// --- DELETE /api/v1/projects/{id}/conversations/{cid} ---

func (h *ProjectHandler) DeleteConversation(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.DeleteConversation(r.Context(), actorID(r), r.PathValue("cid")); err != nil {
		return projectErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// --- DTO ---

type projectDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	CreatedUTC        string `json:"created_utc"`
	UpdatedUTC        string `json:"updated_utc"`
	ConversationCount int    `json:"conversation_count"`
}

type projectsResp struct {
	Projects []projectDTO `json:"projects"`
}

type createProjectReq struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
}

type conversationSummaryDTO struct {
	ID           string `json:"id"`
	ClientID     string `json:"client_id"`
	Title        string `json:"title"`
	UpdatedUTC   string `json:"updated_utc"`
	MessageCount int    `json:"message_count"`
}

type projectDetailDTO struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Conversations []conversationSummaryDTO `json:"conversations"`
}

type upsertConversationReq struct {
	ClientID   string          `json:"client_id"`
	Title      string          `json:"title"`
	CreatedUTC string          `json:"created_utc"`
	UpdatedUTC string          `json:"updated_utc"`
	Messages   json.RawMessage `json:"messages"`
}

type conversationUpsertedDTO struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id"`
	UpdatedUTC string `json:"updated_utc"`
}

func toProjectDTO(p domainproject.Project) projectDTO {
	return projectDTO{
		ID: p.ID, Name: p.Name,
		CreatedUTC: fmtUTC(p.CreatedUTC), UpdatedUTC: fmtUTC(p.UpdatedUTC),
		ConversationCount: p.ConversationCount,
	}
}

func toConversationSummaryDTO(c domainproject.Conversation) conversationSummaryDTO {
	return conversationSummaryDTO{
		ID: c.ID, ClientID: c.ClientID, Title: c.Title,
		UpdatedUTC: fmtUTC(c.UpdatedUTC), MessageCount: c.MessageCount,
	}
}

func fmtUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseUTC(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func messageCount(raw json.RawMessage) int {
	var arr []json.RawMessage
	if len(raw) > 0 && json.Unmarshal(raw, &arr) == nil {
		return len(arr)
	}
	return 0
}

// normalizeMessages 는 nil 메시지를 빈 JSON 배열로 보정한다.
func normalizeMessages(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("[]")
	}
	return raw
}

func projectErrToHTTP(err error) error {
	var ve *domainproject.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainproject.ErrSessionLimitExceeded):
		return ErrTooManyRequests("session storage limit exceeded")
	case errors.Is(err, domainproject.ErrNotFound):
		return ErrNotFound("not found")
	default:
		return err
	}
}
