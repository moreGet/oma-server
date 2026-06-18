package security

import (
	"encoding/json"
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
