package httpin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// AuthHandler 는 로그인 + 멤버 관리 HTTP 핸들러다.
type AuthHandler struct {
	svc domainauth.Service
}

// NewAuthHandler 는 AuthHandler 를 생성한다.
func NewAuthHandler(svc domainauth.Service) *AuthHandler { return &AuthHandler{svc: svc} }

// --- POST /api/v1/auth/login ---

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	token, member, err := h.svc.Login(r.Context(), domainauth.LoginCommand{
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, loginResp{Token: token, Member: toMemberResp(member)})
	return nil
}

// --- GET /api/v1/members ---

func (h *AuthHandler) ListMembers(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	q := r.URL.Query()
	limit := clampLimit(atoiDefault(q.Get("limit"), defaultPageLimit))
	offset := atoiDefault(q.Get("offset"), 0)
	filter := domainauth.MemberFilter{
		RoleID: atoiDefault(q.Get("role_id"), 0),
		Limit:  limit,
		Offset: offset,
	}
	members, total, err := h.svc.ListMembers(r.Context(), claims.MemberID, filter)
	if err != nil {
		return authErrToHTTP(err)
	}
	items := make([]memberResp, 0, len(members))
	for _, m := range members {
		items = append(items, toMemberResp(m))
	}
	writeJSON(w, http.StatusOK, memberListResp{Total: total, Limit: limit, Offset: offset, Items: items})
	return nil
}

// --- GET /api/v1/members/{id} ---

func (h *AuthHandler) GetMember(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	id := r.PathValue("id")
	member, err := h.svc.GetMember(r.Context(), claims.MemberID, id)
	if err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toMemberResp(member))
	return nil
}

// --- POST /api/v1/members ---

func (h *AuthHandler) CreateMember(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req createMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	member, err := h.svc.CreateMember(r.Context(), domainauth.CreateMemberCommand{
		Username: req.Username,
		Password: req.Password,
		RoleID:   req.RoleID,
		ActorID:  claims.MemberID,
	})
	if err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, toMemberResp(member))
	return nil
}

// --- PUT /api/v1/members/{id}/role ---

func (h *AuthHandler) ChangeRole(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req changeRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	member, err := h.svc.ChangeRole(r.Context(), domainauth.ChangeRoleCommand{
		ActorID:  claims.MemberID,
		TargetID: r.PathValue("id"),
		RoleID:   req.RoleID,
	})
	if err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toMemberResp(member))
	return nil
}

// --- PUT /api/v1/members/{id}/active ---

func (h *AuthHandler) SetActive(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req setActiveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	member, err := h.svc.SetActive(r.Context(), domainauth.SetActiveCommand{
		ActorID:  claims.MemberID,
		TargetID: r.PathValue("id"),
		Active:   req.Active,
	})
	if err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toMemberResp(member))
	return nil
}

// --- DELETE /api/v1/members/{id} ---

func (h *AuthHandler) DeleteMember(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	if err := h.svc.DeleteMember(r.Context(), claims.MemberID, r.PathValue("id")); err != nil {
		return authErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// ---------------------------------------------------------------------------
// DTO (snake_case, PasswordHash 노출 금지)
// ---------------------------------------------------------------------------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResp struct {
	Token  string     `json:"token"`
	Member memberResp `json:"member"`
}

type createMemberReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	RoleID   int    `json:"role_id"`
}

type changeRoleReq struct {
	RoleID int `json:"role_id"`
}

type setActiveReq struct {
	Active bool `json:"active"`
}

type roleResp struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Level int    `json:"level"`
}

type memberResp struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Active    bool      `json:"active"`
	Role      roleResp  `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

type memberListResp struct {
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
	Items  []memberResp `json:"items"`
}

// toMemberResp 는 도메인 Member 를 응답 DTO 로 변환한다(PasswordHash 제외).
func toMemberResp(m domainauth.Member) memberResp {
	return memberResp{
		ID:        m.ID,
		Username:  m.Username,
		Active:    m.Active,
		Role:      roleResp{ID: m.Role.ID, Name: m.Role.Name, Level: int(m.Role.Level)},
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
		CreatedBy: m.CreatedBy,
		UpdatedBy: m.UpdatedBy,
	}
}

// authErrToHTTP 는 auth 도메인 에러를 AppError 로 매핑한다.
func authErrToHTTP(err error) error {
	var ve *domainauth.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainauth.ErrNotFound):
		return ErrNotFound("member not found")
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("permission denied")
	case errors.Is(err, domainauth.ErrInvalidCredentials), errors.Is(err, domainauth.ErrInvalidToken):
		return ErrUnauthorized("invalid credentials")
	case errors.Is(err, domainauth.ErrConflict):
		return ErrConflict("member already exists")
	default:
		return err
	}
}
