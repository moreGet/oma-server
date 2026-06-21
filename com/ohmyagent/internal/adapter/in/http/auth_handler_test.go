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
	return s.getMember, s.getMemberErr
}

func (s *fakeAuthService) SetActive(ctx context.Context, cmd domainauth.SetActiveCommand) (domainauth.Member, error) {
	return s.getMember, s.getMemberErr
}

func (s *fakeAuthService) DeleteMember(ctx context.Context, actorID, targetID string) error {
	return s.getMemberErr
}

func (s *fakeAuthService) ChangePassword(ctx context.Context, actorID, oldPassword, newPassword string) error {
	return s.getMemberErr
}
func (s *fakeAuthService) ResetPassword(ctx context.Context, actorID, targetID, newPassword string) error {
	return s.getMemberErr
}
func (s *fakeAuthService) ListRoles(ctx context.Context) ([]domainauth.Role, error) {
	return nil, nil
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
	r.Secured("GET /api/v1/members/{id}", Handle(h.GetMember), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("POST /api/v1/members", Handle(h.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
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

// assertAppErrorCode decodes the AppError envelope and asserts its code.
func assertAppErrorCode(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var ae AppError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ae))
	assert.Equal(t, code, ae.Code)
}
