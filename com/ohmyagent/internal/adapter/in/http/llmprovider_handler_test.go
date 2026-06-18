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
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// fakeProviderService implements domainllmprovider.Service.
type fakeProviderService struct {
	list      []domainllmprovider.LLMProvider
	listErr   error
	get       domainllmprovider.LLMProvider
	getErr    error
	created   domainllmprovider.LLMProvider
	createErr error
	deleteErr error
}

var _ domainllmprovider.Service = (*fakeProviderService)(nil)

func (s *fakeProviderService) List(ctx context.Context, actorID string) ([]domainllmprovider.LLMProvider, error) {
	return s.list, s.listErr
}
func (s *fakeProviderService) Get(ctx context.Context, actorID, id string) (domainllmprovider.LLMProvider, error) {
	return s.get, s.getErr
}
func (s *fakeProviderService) Create(ctx context.Context, cmd domainllmprovider.CreateCommand) (domainllmprovider.LLMProvider, error) {
	return s.created, s.createErr
}
func (s *fakeProviderService) UpdateConfig(ctx context.Context, cmd domainllmprovider.UpdateConfigCommand) (domainllmprovider.LLMProvider, error) {
	return s.get, s.getErr
}
func (s *fakeProviderService) Activate(ctx context.Context, cmd domainllmprovider.ActivateCommand) error {
	return s.getErr
}
func (s *fakeProviderService) Delete(ctx context.Context, cmd domainllmprovider.DeleteCommand) error {
	return s.deleteErr
}
func (s *fakeProviderService) GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error) {
	return nil, nil
}

func newTestRouterWithProvider(svc domainllmprovider.Service) (*security.SecureRouter, *security.JWTTokenService) {
	tok := security.NewJWTTokenService("test-secret", time.Hour)
	r := security.NewSecureRouter(http.NewServeMux(), tok)
	h := NewProviderHandler(svc)
	r.Secured("GET /api/v1/llm-providers", Handle(h.List), security.MinRole(domainauth.RoleLevelUser))
	r.Secured("POST /api/v1/llm-providers", Handle(h.Create), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("PUT /api/v1/llm-providers/{id}/activate", Handle(h.Activate), security.MinRole(domainauth.RoleLevelAdmin))
	r.Secured("DELETE /api/v1/llm-providers/{id}", Handle(h.Delete), security.MinRole(domainauth.RoleLevelAdmin))
	return r, tok
}

func TestProviderHandler_Create(t *testing.T) {
	t.Run("success returns 201", func(t *testing.T) {
		svc := &fakeProviderService{created: domainllmprovider.LLMProvider{
			ID: "p1", Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal,
		}}
		r, tok := newTestRouterWithProvider(svc)

		body, _ := json.Marshal(map[string]any{"name": "ollama", "provider_type": "LOCAL"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/llm-providers", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		var resp providerResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "ollama", resp.Name)
		assert.Equal(t, "LOCAL", resp.ProviderType)
	})

	t.Run("validation error maps to 400", func(t *testing.T) {
		svc := &fakeProviderService{createErr: &domainllmprovider.ErrValidation{Msg: "name is required"}}
		r, tok := newTestRouterWithProvider(svc)

		body, _ := json.Marshal(map[string]any{"name": "", "provider_type": "LOCAL"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/llm-providers", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assertAppErrorCode(t, w, "BAD_REQUEST")
	})

	t.Run("leaked access-gate ErrPermission maps to 403", func(t *testing.T) {
		svc := &fakeProviderService{createErr: domainauth.ErrPermission}
		r, tok := newTestRouterWithProvider(svc)

		body, _ := json.Marshal(map[string]any{"name": "ollama", "provider_type": "LOCAL"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/llm-providers", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assertAppErrorCode(t, w, "FORBIDDEN")
	})
}

func TestProviderHandler_List(t *testing.T) {
	svc := &fakeProviderService{list: []domainllmprovider.LLMProvider{
		{ID: "p1", Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal},
	}}
	r, tok := newTestRouterWithProvider(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/llm-providers", nil)
	req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "u1", domainauth.RoleLevelUser))
	w := httptest.NewRecorder()
	r.Mux().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var items []providerResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "ollama", items[0].Name)
}

func TestProviderHandler_Activate(t *testing.T) {
	t.Run("success returns 200 with message", func(t *testing.T) {
		svc := &fakeProviderService{}
		r, tok := newTestRouterWithProvider(svc)

		req := httptest.NewRequest(http.MethodPut, "/api/v1/llm-providers/p1/activate", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp messageResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEmpty(t, resp.Message)
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		svc := &fakeProviderService{getErr: domainllmprovider.ErrNotFound}
		r, tok := newTestRouterWithProvider(svc)

		req := httptest.NewRequest(http.MethodPut, "/api/v1/llm-providers/missing/activate", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})
}

func TestProviderHandler_Delete(t *testing.T) {
	t.Run("success returns 204", func(t *testing.T) {
		svc := &fakeProviderService{}
		r, tok := newTestRouterWithProvider(svc)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/llm-providers/p1", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, w.Body.String())
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		svc := &fakeProviderService{deleteErr: domainllmprovider.ErrNotFound}
		r, tok := newTestRouterWithProvider(svc)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/llm-providers/missing", nil)
		req.Header.Set("Authorization", "Bearer "+tokenForLevel(t, tok, "admin1", domainauth.RoleLevelAdmin))
		w := httptest.NewRecorder()
		r.Mux().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assertAppErrorCode(t, w, "NOT_FOUND")
	})
}
