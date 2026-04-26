// Package handler_test 는 LLMProviderHandler 의 단위 테스트이다.
//
// httptest + gin.TestMode 로 라우트를 빌드하고, 서비스는 LLMProviderServicePort
// 인터페이스를 통해 testify/mock 으로 대체한다. 도메인 sentinel 에러 → HTTP 상태 매핑,
// JSON 필드명/검증 응답을 검증한다.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/dto"
	"OhMyAgent.AiAgent.Server/internal/handler"
)

// ---------------------------------------------------------------------------
// Mock 서비스 (handler.LLMProviderServicePort 구현)
// ---------------------------------------------------------------------------

type MockLLMProviderService struct {
	mock.Mock
}

func (m *MockLLMProviderService) ListProviders(ctx context.Context) ([]*domain.LLMProvider, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMProviderService) GetProvider(ctx context.Context, id int64) (*domain.LLMProvider, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMProviderService) CreateProvider(ctx context.Context, p *domain.LLMProvider) (*domain.LLMProvider, error) {
	args := m.Called(ctx, p)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMProviderService) ActivateProvider(ctx context.Context, id int64) error {
	return m.Called(ctx, id).Error(0)
}

func (m *MockLLMProviderService) UpdateProviderConfig(ctx context.Context, id int64, c domain.ProviderConfig) error {
	return m.Called(ctx, id, c).Error(0)
}

func (m *MockLLMProviderService) DeleteProvider(ctx context.Context, id int64) error {
	return m.Called(ctx, id).Error(0)
}

// ---------------------------------------------------------------------------
// 헬퍼: 라우터 빌드
// ---------------------------------------------------------------------------

func setupRouter(svc handler.LLMProviderServicePort) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewLLMProviderHandler(svc)
	g := r.Group("/api/v1/admin/llm-providers")
	{
		g.GET("", h.ListProviders)
		g.GET("/:id", h.GetProvider)
		g.POST("", h.CreateProvider)
		g.PUT("/:id/activate", h.ActivateProvider)
		g.PATCH("/:id/config", h.UpdateProviderConfig)
		g.DELETE("/:id", h.DeleteProvider)
	}
	return r
}

func performRequest(r http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sampleProvider(id int64, name string) *domain.LLMProvider {
	return &domain.LLMProvider{
		ID:           id,
		Name:         name,
		IsActive:     true,
		ProviderType: domain.ProviderTypeLocal,
		Config: domain.ProviderConfig{
			Endpoint: "http://localhost:11434",
			Model:    "llama3",
		},
		CreatedAt: time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// ListProviders
// ---------------------------------------------------------------------------

func TestListProviders(t *testing.T) {
	tests := []struct {
		name       string
		mockReturn []*domain.LLMProvider
		mockError  error
		wantStatus int
		wantLen    int
	}{
		{
			name:       "success",
			mockReturn: []*domain.LLMProvider{sampleProvider(1, "p1"), sampleProvider(2, "p2")},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "empty",
			mockReturn: []*domain.LLMProvider{},
			wantStatus: http.StatusOK,
			wantLen:    0,
		},
		{
			name:       "internal_error",
			mockReturn: nil,
			mockError:  errors.New("db down"),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			svc.On("ListProviders", mock.Anything).Return(tt.mockReturn, tt.mockError).Once()

			r := setupRouter(svc)
			w := performRequest(r, http.MethodGet, "/api/v1/admin/llm-providers", nil)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var got []dto.ProviderResponse
				assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Len(t, got, tt.wantLen)
			}
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// GetProvider
// ---------------------------------------------------------------------------

func TestGetProvider(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mockReturn *domain.LLMProvider
		mockError  error
		setupMock  bool
		wantStatus int
	}{
		{
			name:       "success",
			path:       "/api/v1/admin/llm-providers/1",
			mockReturn: sampleProvider(1, "p1"),
			setupMock:  true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid_id_alpha",
			path:       "/api/v1/admin/llm-providers/abc",
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "not_found_wrapped_error",
			path:       "/api/v1/admin/llm-providers/42",
			mockError:  fmt.Errorf("layer: %w", domain.ErrProviderNotFound),
			setupMock:  true,
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			if tt.setupMock {
				svc.On("GetProvider", mock.Anything, mock.AnythingOfType("int64")).
					Return(tt.mockReturn, tt.mockError).Once()
			}

			r := setupRouter(svc)
			w := performRequest(r, http.MethodGet, tt.path, nil)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var got dto.ProviderResponse
				assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, tt.mockReturn.ID, got.ID)
				assert.Equal(t, tt.mockReturn.Name, got.Name)
				assert.Equal(t, string(tt.mockReturn.ProviderType), got.ProviderType)
			}
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// CreateProvider
// ---------------------------------------------------------------------------

func TestCreateProvider(t *testing.T) {
	validBody := `{
		"name":"new",
		"provider_type":"LOCAL",
		"is_active":false,
		"config":{"endpoint":"http://x","model":"m"}
	}`
	tests := []struct {
		name       string
		body       string
		mockReturn *domain.LLMProvider
		mockError  error
		setupMock  bool
		wantStatus int
	}{
		{
			name:       "success",
			body:       validBody,
			mockReturn: sampleProvider(10, "new"),
			setupMock:  true,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "missing_name",
			body:       `{"provider_type":"LOCAL","config":{}}`,
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid_provider_type",
			body:       `{"name":"x","provider_type":"WAT","config":{}}`,
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "conflict",
			body:       validBody,
			mockError:  fmt.Errorf("dup: %w", domain.ErrProviderConflict),
			setupMock:  true,
			wantStatus: http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			if tt.setupMock {
				svc.On("CreateProvider", mock.Anything, mock.AnythingOfType("*domain.LLMProvider")).
					Return(tt.mockReturn, tt.mockError).Once()
			}

			r := setupRouter(svc)
			w := performRequest(r, http.MethodPost, "/api/v1/admin/llm-providers", []byte(tt.body))

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusCreated {
				var got dto.ProviderResponse
				assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, tt.mockReturn.ID, got.ID)
			}
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// ActivateProvider
// ---------------------------------------------------------------------------

func TestActivateProvider(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mockError  error
		setupMock  bool
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "success",
			path:       "/api/v1/admin/llm-providers/1/activate",
			setupMock:  true,
			wantStatus: http.StatusOK,
			wantMsg:    "provider activated",
		},
		{
			name:       "not_found",
			path:       "/api/v1/admin/llm-providers/99/activate",
			mockError:  domain.ErrProviderNotFound,
			setupMock:  true,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "invalid_id",
			path:       "/api/v1/admin/llm-providers/abc/activate",
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			if tt.setupMock {
				svc.On("ActivateProvider", mock.Anything, mock.AnythingOfType("int64")).
					Return(tt.mockError).Once()
			}

			r := setupRouter(svc)
			w := performRequest(r, http.MethodPut, tt.path, nil)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantMsg != "" {
				var msg dto.MessageResponse
				assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &msg))
				assert.Equal(t, tt.wantMsg, msg.Message)
			}
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateProviderConfig
// ---------------------------------------------------------------------------

func TestUpdateProviderConfig(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		mockError  error
		setupMock  bool
		wantStatus int
	}{
		{
			name:       "success",
			path:       "/api/v1/admin/llm-providers/1/config",
			body:       `{"config":{"endpoint":"http://new","model":"gpt-4"}}`,
			setupMock:  true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "empty_body",
			path:       "/api/v1/admin/llm-providers/1/config",
			body:       ``,
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid_id",
			path:       "/api/v1/admin/llm-providers/abc/config",
			body:       `{"config":{}}`,
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "not_found",
			path:       "/api/v1/admin/llm-providers/99/config",
			body:       `{"config":{"model":"gpt-4"}}`,
			mockError:  fmt.Errorf("not exist: %w", domain.ErrProviderNotFound),
			setupMock:  true,
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			if tt.setupMock {
				svc.On("UpdateProviderConfig",
					mock.Anything,
					mock.AnythingOfType("int64"),
					mock.AnythingOfType("domain.ProviderConfig"),
				).Return(tt.mockError).Once()
			}

			r := setupRouter(svc)
			var bodyBytes []byte
			if tt.body != "" {
				bodyBytes = []byte(tt.body)
			}
			w := performRequest(r, http.MethodPatch, tt.path, bodyBytes)

			assert.Equal(t, tt.wantStatus, w.Code)
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// DeleteProvider
// ---------------------------------------------------------------------------

func TestDeleteProvider(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mockError  error
		setupMock  bool
		wantStatus int
	}{
		{
			name:       "success",
			path:       "/api/v1/admin/llm-providers/1",
			setupMock:  true,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "not_found",
			path:       "/api/v1/admin/llm-providers/99",
			mockError:  domain.ErrProviderNotFound,
			setupMock:  true,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "invalid_id",
			path:       "/api/v1/admin/llm-providers/-1",
			setupMock:  false,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := new(MockLLMProviderService)
			if tt.setupMock {
				svc.On("DeleteProvider", mock.Anything, mock.AnythingOfType("int64")).
					Return(tt.mockError).Once()
			}

			r := setupRouter(svc)
			w := performRequest(r, http.MethodDelete, tt.path, nil)

			assert.Equal(t, tt.wantStatus, w.Code)
			svc.AssertExpectations(t)
		})
	}
}

// ---------------------------------------------------------------------------
// 회귀: errors.Is 체인 인식
// ---------------------------------------------------------------------------

func TestRespondError_WrappedSentinelDetection(t *testing.T) {
	svc := new(MockLLMProviderService)
	wrapped := fmt.Errorf("repo: %w", domain.ErrNoActiveProvider)
	svc.On("GetProvider", mock.Anything, mock.AnythingOfType("int64")).
		Return(nil, wrapped).Once()

	r := setupRouter(svc)
	w := performRequest(r, http.MethodGet, "/api/v1/admin/llm-providers/1", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
