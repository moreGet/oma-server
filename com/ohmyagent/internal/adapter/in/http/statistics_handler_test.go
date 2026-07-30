package httpin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// newTestRouterWithStats 는 main.go 와 동일한 게이트(admin↑)로 통계 라우트를 등록한다.
func newTestRouterWithStats(auth domainauth.Service, providers domainllmprovider.Service) (*security.SecureRouter, *security.JWTTokenService) {
	tok := security.NewJWTTokenService("test-secret", time.Hour)
	r := security.NewSecureRouter(http.NewServeMux(), tok)
	h := NewStatsHandler(auth, providers)
	r.Secured("GET /api/v1/statistics", Handle(h.Get), security.MinRole(domainauth.RoleLevelAdmin))
	return r, tok
}

// statsRespBody 는 응답 JSON 의 검증용 형태다(핸들러 내부 DTO 와 독립 — 스키마가 계약임을 드러낸다).
type statsRespBody struct {
	Members struct {
		Total  int            `json:"total"`
		ByRole map[string]int `json:"by_role"`
	} `json:"members"`
	Providers struct {
		Total  int    `json:"total"`
		Active string `json:"active"`
	} `json:"providers"`
}

func getStats(t *testing.T, r *security.SecureRouter, tok *security.JWTTokenService, actorID string, level domainauth.RoleLevel) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/statistics", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, actorID, level))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)
	return w
}

func decodeStats(t *testing.T, w *httptest.ResponseRecorder) statsRespBody {
	t.Helper()
	var body statsRespBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestStatsHandler_Get(t *testing.T) {
	t.Run("admin 성공: 역할별 집계와 활성 provider 를 반환하고 actor 를 전파한다", func(t *testing.T) {
		auth := &fakeAuthService{
			counts: map[int]int{
				domainauth.RoleIDUser:       7,
				domainauth.RoleIDAdmin:      2,
				domainauth.RoleIDSuperAdmin: 1,
			},
			countsTotal: 10,
		}
		providers := &fakeProviderService{list: []domainllmprovider.LLMProvider{
			{ID: "p1", Name: "openai-main", IsActive: false},
			{ID: "p2", Name: "claude-main", IsActive: true},
		}}
		r, tok := newTestRouterWithStats(auth, providers)

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "admin1", auth.lastActorID, "actor 가 use case 로 전파되어야 한다")

		body := decodeStats(t, w)
		assert.Equal(t, 10, body.Members.Total)
		assert.Equal(t, map[string]int{"user": 7, "admin": 2, "super_admin": 1}, body.Members.ByRole)
		assert.Equal(t, 2, body.Providers.Total)
		assert.Equal(t, "claude-main", body.Providers.Active, "IsActive 인 첫 provider 이름")
	})

	t.Run("인원이 0인 역할도 by_role 키로 노출한다(응답 스키마 고정)", func(t *testing.T) {
		auth := &fakeAuthService{
			counts:      map[int]int{domainauth.RoleIDUser: 3},
			countsTotal: 3,
		}
		r, tok := newTestRouterWithStats(auth, &fakeProviderService{})

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		require.Equal(t, http.StatusOK, w.Code)
		body := decodeStats(t, w)
		assert.Equal(t, map[string]int{"user": 3, "admin": 0, "super_admin": 0}, body.Members.ByRole,
			"admin/super_admin 이 0명이어도 키가 사라지면 안 된다")
	})

	t.Run("카탈로그 밖 role_id 는 user 로 접어 넣는다", func(t *testing.T) {
		// NameForRoleID 의 default 폴백과 동일한 취급이다. 집계를 GROUP BY 로 옮기면서
		// 이 폴백이 사라지기 쉬운 자리라 회귀를 고정한다(멤버 총원과 by_role 합이 어긋나면 안 된다).
		auth := &fakeAuthService{
			counts:      map[int]int{domainauth.RoleIDUser: 2, 99: 3},
			countsTotal: 5,
		}
		r, tok := newTestRouterWithStats(auth, &fakeProviderService{})

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		require.Equal(t, http.StatusOK, w.Code)
		body := decodeStats(t, w)
		assert.Equal(t, 5, body.Members.ByRole["user"], "미지의 role_id 3명이 user 로 접혀야 한다")
		assert.Equal(t, 5, body.Members.Total)

		sum := 0
		for _, n := range body.Members.ByRole {
			sum += n
		}
		assert.Equal(t, body.Members.Total, sum, "by_role 합계가 total 과 일치해야 한다")
	})

	t.Run("집계가 비어도 200 과 0 값을 반환한다", func(t *testing.T) {
		auth := &fakeAuthService{counts: map[int]int{}, countsTotal: 0}
		r, tok := newTestRouterWithStats(auth, &fakeProviderService{})

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		require.Equal(t, http.StatusOK, w.Code)
		body := decodeStats(t, w)
		assert.Equal(t, 0, body.Members.Total)
		assert.Equal(t, map[string]int{"user": 0, "admin": 0, "super_admin": 0}, body.Members.ByRole)
		assert.Equal(t, 0, body.Providers.Total)
	})

	t.Run("활성 provider 가 없으면 active 는 생략된다(omitempty)", func(t *testing.T) {
		auth := &fakeAuthService{counts: map[int]int{domainauth.RoleIDUser: 1}, countsTotal: 1}
		providers := &fakeProviderService{list: []domainllmprovider.LLMProvider{
			{ID: "p1", Name: "openai-main", IsActive: false},
		}}
		r, tok := newTestRouterWithStats(auth, providers)

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		require.Equal(t, http.StatusOK, w.Code)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
		prov, ok := raw["providers"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(1), prov["total"])
		assert.NotContains(t, prov, "active", "활성 provider 가 없으면 active 키가 없어야 한다")
	})

	t.Run("멤버 집계 인가 실패는 403 으로 매핑된다", func(t *testing.T) {
		auth := &fakeAuthService{countsErr: domainauth.ErrPermission}
		r, tok := newTestRouterWithStats(auth, &fakeProviderService{})

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assertAppErrorCode(t, w, "FORBIDDEN")
	})

	t.Run("provider 조회 실패는 provider 매핑을 따른다", func(t *testing.T) {
		auth := &fakeAuthService{counts: map[int]int{domainauth.RoleIDUser: 1}, countsTotal: 1}
		providers := &fakeProviderService{listErr: domainllmprovider.ErrUpstream}
		r, tok := newTestRouterWithStats(auth, providers)

		w := getStats(t, r, tok, "admin1", domainauth.RoleLevelAdmin)

		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("user 역할은 라우트 게이트에서 403", func(t *testing.T) {
		r, tok := newTestRouterWithStats(&fakeAuthService{}, &fakeProviderService{})

		w := getStats(t, r, tok, "u1", domainauth.RoleLevelUser)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("토큰이 없으면 401", func(t *testing.T) {
		r, _ := newTestRouterWithStats(&fakeAuthService{}, &fakeProviderService{})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/statistics", nil)
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}
