// Package application_test 는 LLMProviderService 의 단위 테스트이다.
//
// 의존성(LLMRepository, ProviderCache, LLMFactory)을 testify/mock 으로 대체하고,
// 캐시 hit/miss, 활성 전환 시 무효화, 에러 전파 경로를 검증한다.
package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"OhMyAgent.AiAgent.Server/internal/application"
	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// ---------------------------------------------------------------------------
// Mock 구현체
// ---------------------------------------------------------------------------

// MockLLMRepository 는 port.LLMRepository 의 mock 이다.
type MockLLMRepository struct {
	mock.Mock
}

func (m *MockLLMRepository) GetActiveProvider(ctx context.Context) (*domain.LLMProvider, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMRepository) GetProviderByID(ctx context.Context, id int64) (*domain.LLMProvider, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMRepository) ListProviders(ctx context.Context) ([]*domain.LLMProvider, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMRepository) CreateProvider(ctx context.Context, provider *domain.LLMProvider) (*domain.LLMProvider, error) {
	args := m.Called(ctx, provider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.LLMProvider), args.Error(1)
}

func (m *MockLLMRepository) ActivateProvider(ctx context.Context, id int64) error {
	return m.Called(ctx, id).Error(0)
}

func (m *MockLLMRepository) UpdateProviderConfig(ctx context.Context, id int64, config domain.ProviderConfig) error {
	return m.Called(ctx, id, config).Error(0)
}

func (m *MockLLMRepository) DeleteProvider(ctx context.Context, id int64) error {
	return m.Called(ctx, id).Error(0)
}

// MockProviderCache 는 port.ProviderCache 의 mock 이다.
type MockProviderCache struct {
	mock.Mock
}

func (m *MockProviderCache) Get() *domain.LLMProvider {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*domain.LLMProvider)
}

func (m *MockProviderCache) Set(provider *domain.LLMProvider) {
	m.Called(provider)
}

func (m *MockProviderCache) Invalidate() {
	m.Called()
}

// MockLLMFactory 는 port.LLMFactory 의 mock 이다.
type MockLLMFactory struct {
	mock.Mock
}

func (m *MockLLMFactory) CreateAdapter(provider *domain.LLMProvider) (port.LLMAdapter, error) {
	args := m.Called(provider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(port.LLMAdapter), args.Error(1)
}

// MockLLMAdapter 는 port.LLMAdapter 의 mock 이다.
type MockLLMAdapter struct {
	mock.Mock
}

func (m *MockLLMAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	args := m.Called(ctx, prompt)
	return args.String(0), args.Error(1)
}

func (m *MockLLMAdapter) ProviderType() domain.ProviderType {
	return m.Called().Get(0).(domain.ProviderType)
}

// ---------------------------------------------------------------------------
// 헬퍼
// ---------------------------------------------------------------------------

func newProvider(id int64, name string, isActive bool, ptype domain.ProviderType) *domain.LLMProvider {
	return &domain.LLMProvider{
		ID:           id,
		Name:         name,
		IsActive:     isActive,
		ProviderType: ptype,
		Config:       domain.ProviderConfig{Endpoint: "http://x", Model: "m"},
	}
}

// ---------------------------------------------------------------------------
// GetActiveLLMAdapter
// ---------------------------------------------------------------------------

func TestGetActiveLLMAdapter_CacheHit(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)
	adapter := new(MockLLMAdapter)

	provider := newProvider(1, "p1", true, domain.ProviderTypeLocal)
	cache.On("Get").Return(provider).Once()
	factory.On("CreateAdapter", provider).Return(port.LLMAdapter(adapter), nil).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	got, err := svc.GetActiveLLMAdapter(context.Background())

	assert.NoError(t, err)
	assert.NotNil(t, got)
	repo.AssertNotCalled(t, "GetActiveProvider", mock.Anything)
	cache.AssertNotCalled(t, "Set", mock.Anything)
	cache.AssertExpectations(t)
	factory.AssertExpectations(t)
}

func TestGetActiveLLMAdapter_CacheMiss(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)
	adapter := new(MockLLMAdapter)

	provider := newProvider(2, "p2", true, domain.ProviderTypeExternal)
	cache.On("Get").Return((*domain.LLMProvider)(nil)).Once()
	repo.On("GetActiveProvider", mock.Anything).Return(provider, nil).Once()
	cache.On("Set", provider).Return().Once()
	factory.On("CreateAdapter", provider).Return(port.LLMAdapter(adapter), nil).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	got, err := svc.GetActiveLLMAdapter(context.Background())

	assert.NoError(t, err)
	assert.NotNil(t, got)
	repo.AssertExpectations(t)
	cache.AssertExpectations(t)
	factory.AssertExpectations(t)
}

func TestGetActiveLLMAdapter_NoActiveProvider(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	cache.On("Get").Return((*domain.LLMProvider)(nil)).Once()
	repo.On("GetActiveProvider", mock.Anything).
		Return(nil, fmt.Errorf("wrap: %w", domain.ErrNoActiveProvider)).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	_, err := svc.GetActiveLLMAdapter(context.Background())

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNoActiveProvider))
	repo.AssertExpectations(t)
	cache.AssertNotCalled(t, "Set", mock.Anything)
	factory.AssertNotCalled(t, "CreateAdapter", mock.Anything)
}

// ---------------------------------------------------------------------------
// ActivateProvider
// ---------------------------------------------------------------------------

func TestActivateProvider_Success(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	repo.On("ActivateProvider", mock.Anything, int64(7)).Return(nil).Once()
	cache.On("Invalidate").Return().Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.ActivateProvider(context.Background(), 7)

	assert.NoError(t, err)
	repo.AssertExpectations(t)
	cache.AssertExpectations(t)
}

func TestActivateProvider_NotFound(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	repo.On("ActivateProvider", mock.Anything, int64(99)).
		Return(fmt.Errorf("wrap: %w", domain.ErrProviderNotFound)).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.ActivateProvider(context.Background(), 99)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrProviderNotFound))
	repo.AssertExpectations(t)
	cache.AssertNotCalled(t, "Invalidate")
}

// ---------------------------------------------------------------------------
// CreateProvider
// ---------------------------------------------------------------------------

func TestCreateProvider_Success(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	in := newProvider(0, "new", false, domain.ProviderTypeLocal)
	out := newProvider(11, "new", false, domain.ProviderTypeLocal)
	repo.On("CreateProvider", mock.Anything, in).Return(out, nil).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	got, err := svc.CreateProvider(context.Background(), in)

	assert.NoError(t, err)
	assert.Equal(t, int64(11), got.ID)
	repo.AssertExpectations(t)
}

func TestCreateProvider_ValidationFailure_NoRepoCall(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	// Name 비어있어 도메인 Validate 가 실패해야 함.
	in := &domain.LLMProvider{Name: "", ProviderType: domain.ProviderTypeLocal}
	svc := application.NewLLMProviderService(repo, cache, factory)
	_, err := svc.CreateProvider(context.Background(), in)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidProvider))
	repo.AssertNotCalled(t, "CreateProvider", mock.Anything, mock.Anything)
}

// ---------------------------------------------------------------------------
// DeleteProvider
// ---------------------------------------------------------------------------

func TestDeleteProvider_Success(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	repo.On("DeleteProvider", mock.Anything, int64(5)).Return(nil).Once()
	// 캐시가 비어있을 때는 Invalidate 호출 안 함.
	cache.On("Get").Return((*domain.LLMProvider)(nil)).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.DeleteProvider(context.Background(), 5)

	assert.NoError(t, err)
	repo.AssertExpectations(t)
	cache.AssertNotCalled(t, "Invalidate")
}

func TestDeleteProvider_InvalidatesWhenCachedMatches(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	cached := newProvider(5, "cached", true, domain.ProviderTypeLocal)
	repo.On("DeleteProvider", mock.Anything, int64(5)).Return(nil).Once()
	cache.On("Get").Return(cached).Once()
	cache.On("Invalidate").Return().Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.DeleteProvider(context.Background(), 5)

	assert.NoError(t, err)
	repo.AssertExpectations(t)
	cache.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// UpdateProviderConfig
// ---------------------------------------------------------------------------

func TestUpdateProviderConfig_NoCacheInvalidationWhenCacheEmpty(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	cfg := domain.ProviderConfig{Endpoint: "http://new"}
	repo.On("UpdateProviderConfig", mock.Anything, int64(3), cfg).Return(nil).Once()
	cache.On("Get").Return((*domain.LLMProvider)(nil)).Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.UpdateProviderConfig(context.Background(), 3, cfg)

	assert.NoError(t, err)
	repo.AssertExpectations(t)
	cache.AssertNotCalled(t, "Invalidate")
}

func TestUpdateProviderConfig_InvalidatesWhenCachedIDMatches(t *testing.T) {
	repo := new(MockLLMRepository)
	cache := new(MockProviderCache)
	factory := new(MockLLMFactory)

	cfg := domain.ProviderConfig{Model: "claude"}
	cached := newProvider(3, "cached", true, domain.ProviderTypeExternal)
	repo.On("UpdateProviderConfig", mock.Anything, int64(3), cfg).Return(nil).Once()
	cache.On("Get").Return(cached).Once()
	cache.On("Invalidate").Return().Once()

	svc := application.NewLLMProviderService(repo, cache, factory)
	err := svc.UpdateProviderConfig(context.Background(), 3, cfg)

	assert.NoError(t, err)
	cache.AssertExpectations(t)
}
