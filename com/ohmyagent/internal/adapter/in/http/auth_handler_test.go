package httpin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// ---------------------------------------------------------------------------
// fake auth service (domainauth.Service)
// ---------------------------------------------------------------------------

type fakeAuthService struct {
	loginToken  string
	loginMember domainauth.Member
	loginErr    error

	getMember    domainauth.Member
	getMemberErr error

	createMember    domainauth.Member
	createMemberErr error

	rolesResult []domainauth.Role

	lastActorID string
}

var _ domainauth.Service = (*fakeAuthService)(nil)

func (s *fakeAuthService) Login(ctx context.Context, cmd domainauth.LoginCommand) (string, domainauth.Member, error) {
	if s.loginErr != nil {
		return "", domainauth.Member{}, s.loginErr
	}
	return s.loginToken, s.loginMember, nil
}

func (s *fakeAuthService) ListMembers(ctx context.Context, actorID string, filter domainauth.MemberFilter) ([]domainauth.Member, int, error) {
	s.lastActorID = actorID
	return []domainauth.Member{s.getMember}, 1, nil
}

func (s *fakeAuthService) GetMember(ctx context.Context, actorID, targetID string) (domainauth.Member, error) {
	s.lastActorID = actorID
	return s.getMember, s.getMemberErr
}

func (s *fakeAuthService) CreateMember(ctx context.Context, cmd domainauth.CreateMemberCommand) (domainauth.Member, error) {
	s.lastActorID = cmd.ActorID
	return s.createMember, s.createMemberErr
}

func (s *fakeAuthService) ChangeRole(ctx context.Context, cmd domainauth.ChangeRoleCommand) (domainauth.Member, error) {
	s.lastActorID = cmd.ActorID
	return s.getMember, s.getMemberErr
}

func (s *fakeAuthService) SetActive(ctx context.Context, cmd domainauth.SetActiveCommand) (domainauth.Member, error) {
	s.lastActorID = cmd.ActorID
	return s.getMember, s.getMemberErr
}

func (s *fakeAuthService) DeleteMember(ctx context.Context, actorID, targetID string) error {
	s.lastActorID = actorID
	return s.getMemberErr
}

func (s *fakeAuthService) ChangePassword(ctx context.Context, actorID, oldPassword, newPassword string) error {
	return s.getMemberErr
}
func (s *fakeAuthService) ResetPassword(ctx context.Context, actorID, targetID, newPassword string) error {
	return s.getMemberErr
}
func (s *fakeAuthService) ListRoles(ctx context.Context) ([]domainauth.Role, error) {
	return s.rolesResult, nil
}

func (s *fakeAuthService) RequireActiveMember(ctx context.Context, actorID string) (domainauth.Member, error) {
	return domainauth.Member{}, nil
}
func (s *fakeAuthService) RequireAdmin(ctx context.Context, actorID string) error { return nil }
func (s *fakeAuthService) EnsureProjectAccess(ctx context.Context, actorID string, projectID int) error {
	return nil
}

// ---------------------------------------------------------------------------
// helpers: build a SecureRouter so claims land in context, and a valid token.
// ---------------------------------------------------------------------------

func newTestRouterWithAuth(svc domainauth.Service) (*security.SecureRouter, *security.JWTTokenService) {
	tok := security.NewJWTTokenService("test-secret", time.Hour)
	r := security.NewSecureRouter(http.NewServeMux(), tok)
	h := NewAuthHandler(svc)
	r.Public("POST /api/v1/auth/login", Handle(h.Login))
	r.Secured("GET /api/v1/me", Handle(h.Me), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("PUT /api/v1/me/password", Handle(h.ChangePassword), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("GET /api/v1/roles", Handle(h.ListRoles), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("GET /api/v1/members", Handle(h.ListMembers), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("GET /api/v1/members/{id}", Handle(h.GetMember), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("POST /api/v1/members", Handle(h.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("PUT /api/v1/members/{id}/role", Handle(h.ChangeRole), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("PUT /api/v1/members/{id}/active", Handle(h.SetActive), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("PUT /api/v1/members/{id}/password", Handle(h.ResetPassword), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("DELETE /api/v1/members/{id}", Handle(h.DeleteMember), security.MinRole(domainauth.RoleLevelSuperAdmin))
	return r, tok
}

func tokenForLevel(t *testing.T, tok *security.JWTTokenService, id string, level domainauth.RoleLevel) string {
	t.Helper()
	s, err := tok.Generate(domainauth.Member{ID: id, Username: "u", Role: domainauth.Role{Level: level}})
	require.NoError(t, err)
	return s
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestAuthHandler_Login(t *testing.T) {
	t.Run("success returns 200 with token and member, no password hash", func(t *testing.T) {
		svc := &fakeAuthService{
			loginToken: "tok-abc",
			loginMember: domainauth.Member{
				ID: "u1", Username: "alice", Active: true, PasswordHash: "SECRET-HASH",
				Role: domainauth.Role{ID: 2, Name: "admin", Level: domainauth.RoleLevelAdmin},
			},
		}
		r, _ := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]string{"username": "alice", "password": "pw"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "SECRET-HASH")
		assert.NotContains(t, w.Body.String(), "password_hash")

		var resp struct {
			Token  string         `json:"token"`
			Member map[string]any `json:"member"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "tok-abc", resp.Token)
		assert.Equal(t, "alice", resp.Member["username"])
		_, hasHash := resp.Member["password_hash"]
		assert.False(t, hasHash)
	})

	t.Run("invalid credentials maps to 401", func(t *testing.T) {
		svc := &fakeAuthService{loginErr: domainauth.ErrInvalidCredentials}
		r, _ := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]string{"username": "alice", "password": "bad"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assertAppErrorCode(t, w, "UNAUTHORIZED")
	})

	t.Run("malformed JSON body maps to 400", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, _ := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader([]byte("{not-json")))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})
}

// ---------------------------------------------------------------------------
// GetMember + claims propagation
// ---------------------------------------------------------------------------

func TestAuthHandler_GetMember(t *testing.T) {
	t.Run("success returns 200 and propagates actor id from claims", func(t *testing.T) {
		svc := &fakeAuthService{
			getMember: domainauth.Member{ID: "u9", Username: "bob", Active: true, PasswordHash: "HASH",
				Role: domainauth.Role{ID: 1, Name: "user", Level: domainauth.RoleLevelUser}},
		}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members/u9", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "actor-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "actor-1", svc.lastActorID)
		assert.NotContains(t, w.Body.String(), "HASH")
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		svc := &fakeAuthService{getMemberErr: domainauth.ErrNotFound}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members/missing", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "actor-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})

	t.Run("permission denied maps to 403", func(t *testing.T) {
		svc := &fakeAuthService{getMemberErr: domainauth.ErrPermission}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members/other", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "actor-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assertAppErrorCode(t, w, "FORBIDDEN")
	})
}

// ---------------------------------------------------------------------------
// CreateMember
// ---------------------------------------------------------------------------

func TestAuthHandler_CreateMember(t *testing.T) {
	t.Run("success returns 201", func(t *testing.T) {
		svc := &fakeAuthService{
			createMember: domainauth.Member{ID: "new1", Username: "carol", Active: true,
				Role: domainauth.Role{ID: 1, Name: "user", Level: domainauth.RoleLevelUser}},
		}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"username": "carol", "password": "password1", "role_id": 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/members", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "admin1", svc.lastActorID)
	})

	t.Run("validation error maps to 400", func(t *testing.T) {
		svc := &fakeAuthService{createMemberErr: &domainauth.ErrValidation{Msg: "password must be >= 8 chars"}}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"username": "carol", "password": "short", "role_id": 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/members", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})

	t.Run("conflict maps to 409", func(t *testing.T) {
		svc := &fakeAuthService{createMemberErr: domainauth.ErrConflict}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"username": "taken", "password": "password1", "role_id": 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/members", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
		assertAppErrorCode(t, w, "CONFLICT")
	})
}

// ---------------------------------------------------------------------------
// ListMembers / ChangeRole / SetActive / DeleteMember / ResetPassword
// ChangePassword / Me / ListRoles  (+ route-level RBAC)
// ---------------------------------------------------------------------------

func TestAuthHandler_ListMembers(t *testing.T) {
	t.Run("admin success 200 propagates actor", func(t *testing.T) {
		svc := &fakeAuthService{getMember: domainauth.Member{ID: "u1", Username: "bob", Active: true,
			Role: domainauth.Role{ID: 1, Name: "user", Level: domainauth.RoleLevelUser}}}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members?limit=10", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "admin1", svc.lastActorID)
		var resp struct {
			Total int              `json:"total"`
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 1, resp.Total)
		require.Len(t, resp.Items, 1)
		assert.Equal(t, "bob", resp.Items[0]["username"])
	})

	t.Run("user role is blocked by route gate (403)", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("missing bearer token is 401", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, _ := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestAuthHandler_ChangeRole(t *testing.T) {
	t.Run("success 200 propagates actor from claims", func(t *testing.T) {
		svc := &fakeAuthService{getMember: domainauth.Member{ID: "u1", Username: "bob",
			Role: domainauth.Role{ID: 2, Name: "admin", Level: domainauth.RoleLevelAdmin}}}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"role_id": 2})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/members/u1/role", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "super1", domainauth.RoleLevelSuperAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "super1", svc.lastActorID)
	})

	t.Run("permission denied maps to 403", func(t *testing.T) {
		svc := &fakeAuthService{getMemberErr: domainauth.ErrPermission}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"role_id": 2})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/members/u1/role", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assertAppErrorCode(t, w, "FORBIDDEN")
	})
}

func TestAuthHandler_SetActive(t *testing.T) {
	svc := &fakeAuthService{getMember: domainauth.Member{ID: "u1", Username: "bob", Active: false,
		Role: domainauth.Role{ID: 1, Name: "user", Level: domainauth.RoleLevelUser}}}
	r, tok := newTestRouterWithAuth(svc)

	body, _ := json.Marshal(map[string]any{"active": false})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/members/u1/active", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "super1", domainauth.RoleLevelSuperAdmin))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "super1", svc.lastActorID)
}

func TestAuthHandler_DeleteMember(t *testing.T) {
	t.Run("super_admin success 204", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/members/u1", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "super1", domainauth.RoleLevelSuperAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "super1", svc.lastActorID)
	})

	t.Run("admin lacks super_admin role, blocked by route gate (403)", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, tok := newTestRouterWithAuth(svc)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/members/u1", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestAuthHandler_ResetPassword(t *testing.T) {
	svc := &fakeAuthService{}
	r, tok := newTestRouterWithAuth(svc)

	body, _ := json.Marshal(map[string]any{"new_password": "newpassword1"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/members/u1/password", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthHandler_ChangePassword(t *testing.T) {
	t.Run("success 200", func(t *testing.T) {
		svc := &fakeAuthService{}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"old_password": "old", "new_password": "newpassword1"})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/me/password", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("wrong old password maps to 401", func(t *testing.T) {
		svc := &fakeAuthService{getMemberErr: domainauth.ErrInvalidCredentials}
		r, tok := newTestRouterWithAuth(svc)

		body, _ := json.Marshal(map[string]any{"old_password": "bad", "new_password": "newpassword1"})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/me/password", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assertAppErrorCode(t, w, "UNAUTHORIZED")
	})
}

func TestAuthHandler_Me(t *testing.T) {
	svc := &fakeAuthService{getMember: domainauth.Member{ID: "u1", Username: "alice", Active: true, PasswordHash: "HASH",
		Role: domainauth.Role{ID: 1, Name: "user", Level: domainauth.RoleLevelUser}}}
	r, tok := newTestRouterWithAuth(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "u1", svc.lastActorID)
	assert.NotContains(t, w.Body.String(), "HASH")
}

func TestAuthHandler_ListRoles(t *testing.T) {
	svc := &fakeAuthService{rolesResult: []domainauth.Role{
		{ID: 1, Name: "user", Level: domainauth.RoleLevelUser},
		{ID: 2, Name: "admin", Level: domainauth.RoleLevelAdmin},
		{ID: 3, Name: "super_admin", Level: domainauth.RoleLevelSuperAdmin},
	}}
	r, tok := newTestRouterWithAuth(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Roles []map[string]any `json:"roles"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Roles, 3)
	assert.Equal(t, "super_admin", resp.Roles[2]["name"])
}

// assertAppErrorCode decodes the AppError envelope and asserts its code.
func assertAppErrorCode(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var ae AppError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ae))
	assert.Equal(t, code, ae.Code)
}
