package security

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

func okHandler(w http.ResponseWriter, r *http.Request) {
	claims, _ := ClaimsFrom(r.Context())
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"member_id": claims.MemberID})
}

func TestSecureRouter_MinRoleGate(t *testing.T) {
	tok := NewJWTTokenService("secret", time.Hour)

	makeToken := func(id string, level domainauth.RoleLevel) string {
		s, err := tok.Generate(domainauth.Member{ID: id, Username: "u", Role: domainauth.Role{Level: level}})
		require.NoError(t, err)
		return s
	}

	tests := []struct {
		name       string
		authHeader string
		minRole    domainauth.RoleLevel
		wantStatus int
		wantCode   string
	}{
		{
			name:       "no token returns 401",
			authHeader: "",
			minRole:    domainauth.RoleLevelUser,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:       "malformed token returns 401",
			authHeader: "Bearer not-a-real-token",
			minRole:    domainauth.RoleLevelUser,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:       "insufficient level returns 403",
			authHeader: "Bearer " + makeToken("u1", domainauth.RoleLevelUser),
			minRole:    domainauth.RoleLevelAdmin,
			wantStatus: http.StatusForbidden,
			wantCode:   "FORBIDDEN",
		},
		{
			name:       "sufficient level passes through",
			authHeader: "Bearer " + makeToken("a1", domainauth.RoleLevelAdmin),
			minRole:    domainauth.RoleLevelAdmin,
			wantStatus: http.StatusOK,
		},
		{
			name:       "higher level passes a lower gate",
			authHeader: "Bearer " + makeToken("s1", domainauth.RoleLevelSuperAdmin),
			minRole:    domainauth.RoleLevelAdmin,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSecureRouter(http.NewServeMux(), tok)
			r.Secured("GET /secure", okHandler, MinRole(tt.minRole))

			req := httptest.NewRequest(http.MethodGet, "/secure", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()
			r.Mux().ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantCode != "" {
				var env map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
				assert.Equal(t, tt.wantCode, env["code"])
			}
		})
	}
}

func TestSecureRouter_ClaimsInjectedOnPass(t *testing.T) {
	tok := NewJWTTokenService("secret", time.Hour)
	s, err := tok.Generate(domainauth.Member{ID: "member-42", Username: "u", Role: domainauth.Role{Level: domainauth.RoleLevelUser}})
	require.NoError(t, err)

	r := NewSecureRouter(http.NewServeMux(), tok)
	r.Secured("GET /secure", okHandler, MinRole(domainauth.RoleLevelUser))

	req := httptest.NewRequest(http.MethodGet, "/secure", nil)
	req.Header.Set("Authorization", "Bearer "+s)
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var env map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, "member-42", env["member_id"])
}

// --- 서비스 계정 API키 인증 경로(oma_sa_ 접두사) 추가 케이스 ---

// fakeAPIKeyAuth 는 APIKeyAuthenticator 페이크다(호출된 raw 기록 + 주입 가능한 Claims/오류).
type fakeAPIKeyAuth struct {
	called bool
	gotRaw string
	claims domainauth.Claims
	err    error
}

var _ APIKeyAuthenticator = (*fakeAPIKeyAuth)(nil)

func (f *fakeAPIKeyAuth) Authenticate(_ context.Context, raw string) (domainauth.Claims, error) {
	f.called = true
	f.gotRaw = raw
	return f.claims, f.err
}

// TestSecureRouter_APIKeyPath 는 oma_sa_ 접두사 토큰이 APIKeyAuthenticator 경로로 분기하고
// 합성 Claims 가 주입됨을 검증한다(JWT Parse 는 타지 않음).
func TestSecureRouter_APIKeyPath(t *testing.T) {
	tok := NewJWTTokenService("secret", time.Hour)

	t.Run("oma_sa_ 접두사 → Authenticate 경로 + Claims 주입", func(t *testing.T) {
		auth := &fakeAPIKeyAuth{claims: domainauth.Claims{MemberID: "sa-1", Username: "bot", Level: domainauth.RoleLevelUser}}
		r := NewSecureRouter(http.NewServeMux(), tok, WithAPIKeyAuth(auth))
		r.Secured("GET /secure", okHandler, MinRole(domainauth.RoleLevelUser))

		req := httptest.NewRequest(http.MethodGet, "/secure", nil)
		req.Header.Set("Authorization", "Bearer oma_sa_opaque-token")
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.True(t, auth.called, "API키 인증기가 호출돼야 한다")
		assert.Equal(t, "oma_sa_opaque-token", auth.gotRaw, "raw 토큰(접두사 포함)을 그대로 전달")
		var env map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		assert.Equal(t, "sa-1", env["member_id"], "합성 Claims 주입")
	})

	t.Run("Authenticate 실패(ErrInvalidToken) → 401", func(t *testing.T) {
		auth := &fakeAPIKeyAuth{err: domainauth.ErrInvalidToken}
		r := NewSecureRouter(http.NewServeMux(), tok, WithAPIKeyAuth(auth))
		r.Secured("GET /secure", okHandler, MinRole(domainauth.RoleLevelUser))

		req := httptest.NewRequest(http.MethodGet, "/secure", nil)
		req.Header.Set("Authorization", "Bearer oma_sa_bad")
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		var env map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		assert.Equal(t, "UNAUTHORIZED", env["code"])
	})

	t.Run("SA 인증 성공이라도 Level<minRole 이면 403(admin 게이트)", func(t *testing.T) {
		auth := &fakeAPIKeyAuth{claims: domainauth.Claims{MemberID: "sa-1", Level: domainauth.RoleLevelUser}}
		r := NewSecureRouter(http.NewServeMux(), tok, WithAPIKeyAuth(auth))
		r.Secured("GET /admin", okHandler, MinRole(domainauth.RoleLevelAdmin))

		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.Header.Set("Authorization", "Bearer oma_sa_opaque")
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("JWT 토큰은 API키 경로를 타지 않는다(무회귀)", func(t *testing.T) {
		auth := &fakeAPIKeyAuth{err: errors.New("must not be called")}
		s, err := tok.Generate(domainauth.Member{ID: "member-7", Username: "u", Role: domainauth.Role{Level: domainauth.RoleLevelUser}})
		require.NoError(t, err)
		r := NewSecureRouter(http.NewServeMux(), tok, WithAPIKeyAuth(auth))
		r.Secured("GET /secure", okHandler, MinRole(domainauth.RoleLevelUser))

		req := httptest.NewRequest(http.MethodGet, "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+s)
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.False(t, auth.called, "JWT 는 oma_sa_ 접두사가 아니므로 Parse 경로")
		var env map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		assert.Equal(t, "member-7", env["member_id"])
	})
}

// TestSecureRouter_APIKeyAuthNil 은 apiKeyAuth 미주입 시 oma_sa_ 접두사 토큰도
// 기존 JWT Parse 경로로 처리됨(구동작 보존)을 검증한다.
func TestSecureRouter_APIKeyAuthNil(t *testing.T) {
	tok := NewJWTTokenService("secret", time.Hour)
	r := NewSecureRouter(http.NewServeMux(), tok) // 옵션 없음 → apiKeyAuth == nil
	r.Secured("GET /secure", okHandler, MinRole(domainauth.RoleLevelUser))

	req := httptest.NewRequest(http.MethodGet, "/secure", nil)
	req.Header.Set("Authorization", "Bearer oma_sa_opaque-token")
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	// oma_sa_ 토큰은 유효한 JWT 가 아니므로 Parse 실패 → 401(API키 경로 비활성).
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
