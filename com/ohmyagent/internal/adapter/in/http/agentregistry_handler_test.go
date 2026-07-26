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
	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// fakeRegistryService 는 domainagentregistry.Service 페이크다.
type fakeRegistryService struct {
	registered   domainagentregistry.Agent
	registerErr  error
	lastRegister domainagentregistry.RegisterCommand
	heartbeatErr error
	lastOwner    string
	deregErr     error
	agents       []domainagentregistry.Agent
	discoverErr  error
	lastFilter   domainagentregistry.Filter
	got          domainagentregistry.Agent
	getErr       error
	token        domainagentregistry.A2AToken
	mintErr      error
	pubKID       string
	pubPEM       string
}

var _ domainagentregistry.Service = (*fakeRegistryService)(nil)

func (s *fakeRegistryService) Register(_ context.Context, cmd domainagentregistry.RegisterCommand) (domainagentregistry.Agent, error) {
	s.lastRegister = cmd
	return s.registered, s.registerErr
}
func (s *fakeRegistryService) Heartbeat(_ context.Context, id, ownerID string) error {
	s.lastOwner = ownerID
	return s.heartbeatErr
}
func (s *fakeRegistryService) Deregister(_ context.Context, id, ownerID string) error {
	return s.deregErr
}
func (s *fakeRegistryService) Discover(_ context.Context, f domainagentregistry.Filter) ([]domainagentregistry.Agent, error) {
	s.lastFilter = f
	return s.agents, s.discoverErr
}
func (s *fakeRegistryService) Get(_ context.Context, id string) (domainagentregistry.Agent, error) {
	return s.got, s.getErr
}
func (s *fakeRegistryService) MintToken(_ context.Context, callerMemberID, targetAgentID string) (domainagentregistry.A2AToken, error) {
	return s.token, s.mintErr
}
func (s *fakeRegistryService) PublicKey() (string, string, string) {
	return s.pubKID, domainagentregistry.A2AAlg, s.pubPEM
}
func (s *fakeRegistryService) LeaseTTL() time.Duration          { return 45 * time.Second }
func (s *fakeRegistryService) HeartbeatInterval() time.Duration { return 15 * time.Second }

func newTestRouterWithRegistry(svc domainagentregistry.Service) (*security.SecureRouter, *security.JWTTokenService) {
	tok := security.NewJWTTokenService("test-secret", time.Hour)
	r := security.NewSecureRouter(http.NewServeMux(), tok)
	h := NewAgentRegistryHandler(svc)
	r.Secured("POST /api/v1/agents/register", Handle(h.Register), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("POST /api/v1/agents/{id}/heartbeat", Handle(h.Heartbeat), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("DELETE /api/v1/agents/{id}", Handle(h.Deregister), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("GET /api/v1/agents", Handle(h.List), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("GET /api/v1/agents/{id}", Handle(h.Get), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("POST /api/v1/agents/{id}/token", Handle(h.MintToken), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("GET /api/v1/agents/a2a-public-key", Handle(h.PublicKey), security.MinRole(domainauth.RoleLevelUser))
	return r, tok
}

func TestAgentRegistryHandler_Register(t *testing.T) {
	t.Run("성공: agent_id + 루프 주기 반환, owner 는 JWT claims", func(t *testing.T) {
		svc := &fakeRegistryService{registered: domainagentregistry.Agent{ID: "a-1"}}
		r, tok := newTestRouterWithRegistry(svc)

		body, _ := json.Marshal(map[string]any{
			"name": "reviewer", "endpoint_url": "http://10.0.0.5:8080",
			"capabilities": []string{"code-review"}, "tags": []string{"prod"},
			"model": "gpt-4o-mini", "version": "1.0.0",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp agentRegisterResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "a-1", resp.AgentID)
		assert.Equal(t, 45, resp.LeaseTTLSeconds)
		assert.Equal(t, 15, resp.HeartbeatIntervalSeconds)
		assert.Equal(t, "member-1", svc.lastRegister.OwnerID, "owner 는 본문이 아닌 JWT 에서")
		assert.Equal(t, "reviewer", svc.lastRegister.Name)
	})

	t.Run("검증 실패 400", func(t *testing.T) {
		svc := &fakeRegistryService{registerErr: &domainagentregistry.ErrValidation{Msg: "endpoint_url must be an absolute http/https URL"}}
		r, tok := newTestRouterWithRegistry(svc)

		body, _ := json.Marshal(map[string]any{"name": "x", "endpoint_url": "ftp://x"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})

	t.Run("인증 없으면 401", func(t *testing.T) {
		r, _ := newTestRouterWithRegistry(&fakeRegistryService{})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register", bytes.NewReader([]byte("{}")))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestAgentRegistryHandler_Heartbeat(t *testing.T) {
	t.Run("성공: status=online + lease_ttl", func(t *testing.T) {
		svc := &fakeRegistryService{}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a-1/heartbeat", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp agentHeartbeatResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "online", resp.Status)
		assert.Equal(t, 45, resp.LeaseTTLSeconds)
		assert.Equal(t, "member-1", svc.lastOwner)
	})

	t.Run("소유자 불일치(ErrForbidden)도 404 로 은닉(§공유 계약: 재-register 자가치유)", func(t *testing.T) {
		svc := &fakeRegistryService{heartbeatErr: domainagentregistry.ErrForbidden}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a-1/heartbeat", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-2", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})

	t.Run("없는 id 404", func(t *testing.T) {
		svc := &fakeRegistryService{heartbeatErr: domainagentregistry.ErrNotFound}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/nope/heartbeat", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestAgentRegistryHandler_Deregister(t *testing.T) {
	svc := &fakeRegistryService{}
	r, tok := newTestRouterWithRegistry(svc)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/a-1", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestAgentRegistryHandler_List(t *testing.T) {
	hb := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	svc := &fakeRegistryService{agents: []domainagentregistry.Agent{{
		ID: "a-1", Name: "reviewer", EndpointURL: "http://10.0.0.5:8080",
		Capabilities: []string{"code-review"}, Model: "gpt-4o-mini",
		Status: domainagentregistry.StatusOnline, LastHeartbeatAt: hb,
	}}}
	r, tok := newTestRouterWithRegistry(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents?capability=code-review&status=online&q=rev&exclude_self=a-9", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// 쿼리 파라미터 → 필터 매핑.
	assert.Equal(t, "code-review", svc.lastFilter.Capability)
	assert.Equal(t, "online", svc.lastFilter.Status)
	assert.Equal(t, "rev", svc.lastFilter.Query)
	assert.Equal(t, "a-9", svc.lastFilter.ExcludeID)

	// §공유 계약 JSON 필드명 확인(스네이크 케이스 + tags 는 null 아닌 []).
	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	agents := raw["agents"].([]any)
	require.Len(t, agents, 1)
	first := agents[0].(map[string]any)
	assert.Equal(t, "a-1", first["agent_id"])
	assert.Equal(t, "http://10.0.0.5:8080", first["endpoint_url"])
	assert.Equal(t, "online", first["status"])
	assert.Equal(t, "2026-07-25T12:00:00Z", first["last_heartbeat_at"], "RFC3339(UTC)")
	assert.Equal(t, []any{}, first["tags"], "빈 태그는 null 이 아닌 빈 배열")
}

func TestAgentRegistryHandler_Get(t *testing.T) {
	t.Run("성공", func(t *testing.T) {
		svc := &fakeRegistryService{got: domainagentregistry.Agent{ID: "a-1", Name: "reviewer", Status: domainagentregistry.StatusStale}}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/a-1", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp agentResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "a-1", resp.AgentID)
		assert.Equal(t, "stale", resp.Status)
	})

	t.Run("없으면 404", func(t *testing.T) {
		svc := &fakeRegistryService{getErr: domainagentregistry.ErrNotFound}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/nope", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestAgentRegistryHandler_MintToken(t *testing.T) {
	t.Run("성공: token + expires_in + audience", func(t *testing.T) {
		svc := &fakeRegistryService{token: domainagentregistry.A2AToken{
			Token: "ey.signed.jwt", ExpiresIn: 120 * time.Second, AudienceAgentID: "a-target",
		}}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a-target/token", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp a2aTokenResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "ey.signed.jwt", resp.Token)
		assert.Equal(t, 120, resp.ExpiresInSeconds)
		assert.Equal(t, "a-target", resp.AudienceAgentID)
	})

	t.Run("대상 미존재 404", func(t *testing.T) {
		svc := &fakeRegistryService{mintErr: domainagentregistry.ErrNotFound}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/nope/token", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestAgentRegistryHandler_PublicKey(t *testing.T) {
	t.Run("성공: kid/alg/public_key_pem", func(t *testing.T) {
		svc := &fakeRegistryService{pubKID: "kid-1", pubPEM: "-----BEGIN PUBLIC KEY-----..."}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/a2a-public-key", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp a2aPublicKeyResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "kid-1", resp.KID)
		assert.Equal(t, "ES256", resp.Alg)
		assert.Equal(t, "-----BEGIN PUBLIC KEY-----...", resp.PublicKeyPEM)
	})

	t.Run("리터럴 경로가 GET /agents/{id} 보다 우선(라우트 충돌 실측)", func(t *testing.T) {
		// {id} 핸들러(fake.got)와 구분되는 공개키 응답이 와야 한다.
		svc := &fakeRegistryService{pubKID: "kid-1", pubPEM: "PEM", got: domainagentregistry.Agent{ID: "should-not-match"}}
		r, tok := newTestRouterWithRegistry(svc)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/a2a-public-key", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "member-1", domainauth.RoleLevelUser))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		_, isAgent := resp["agent_id"]
		assert.False(t, isAgent, "{id} 라우트가 아닌 공개키 라우트로 매칭돼야 한다")
		assert.Equal(t, "kid-1", resp["kid"])
	})
}
