package application

import (
	"context"
	"errors"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// LLMProviderService 는 LLM Provider 관리 + 활성 어댑터 조회 유스케이스를 담당한다.
// 캐시 우선 조회, DB 폴백, 어댑터 생성 조율, 무효화 트리거를 모두 이 서비스에서 수행한다.
type LLMProviderService struct {
	repo    port.LLMRepository
	cache   port.ProviderCache
	factory port.LLMFactory
}

// NewLLMProviderService 는 의존성을 주입받아 서비스 인스턴스를 생성한다.
func NewLLMProviderService(
	repo port.LLMRepository,
	cache port.ProviderCache,
	factory port.LLMFactory,
) *LLMProviderService {
	return &LLMProviderService{repo: repo, cache: cache, factory: factory}
}

// GetActiveLLMAdapter 는 캐시에서 활성 Provider 를 조회하고, 없으면 DB 에서 로드한 후 캐싱한다.
// 매 호출마다 어댑터를 새로 빌드한다 (스텁이라 비용 미미; 실제 구현에서는 어댑터 캐시 추가 가능).
//
// 에러:
//   - domain.ErrNoActiveProvider: 활성 Provider 가 DB 에 존재하지 않음
//   - domain.ErrUnsupportedProvider 등 어댑터 생성 실패
func (s *LLMProviderService) GetActiveLLMAdapter(ctx context.Context) (port.LLMAdapter, error) {
	provider := s.cache.Get()
	if provider == nil {
		var err error
		provider, err = s.repo.GetActiveProvider(ctx)
		if err != nil {
			return nil, err
		}
		s.cache.Set(provider)
	}
	return s.factory.CreateAdapter(provider)
}

// ListProviders 는 전체 Provider 목록을 ID 오름차순으로 반환한다.
func (s *LLMProviderService) ListProviders(ctx context.Context) ([]*domain.LLMProvider, error) {
	return s.repo.ListProviders(ctx)
}

// GetProvider 는 ID 로 단일 Provider 를 조회한다.
func (s *LLMProviderService) GetProvider(ctx context.Context, id int64) (*domain.LLMProvider, error) {
	return s.repo.GetProviderByID(ctx, id)
}

// CreateProvider 는 새 Provider 를 등록하고 채워진 도메인 엔티티를 반환한다.
// 입력 도메인 엔티티는 호출 측이 DTO → domain 매핑 후 전달한다.
func (s *LLMProviderService) CreateProvider(ctx context.Context, provider *domain.LLMProvider) (*domain.LLMProvider, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	return s.repo.CreateProvider(ctx, provider)
}

// ActivateProvider 는 지정 ID 를 활성화하고 (트랜잭션) 캐시를 무효화한다.
// 다음 GetActiveLLMAdapter 호출에서 새 Provider 가 lazy-load 된다.
func (s *LLMProviderService) ActivateProvider(ctx context.Context, id int64) error {
	if err := s.repo.ActivateProvider(ctx, id); err != nil {
		return err
	}
	s.cache.Invalidate()
	return nil
}

// UpdateProviderConfig 는 config 를 갱신한다. 갱신 대상이 현재 활성 Provider 라면
// 캐시도 무효화하여 다음 요청에서 새 config 가 반영되도록 한다.
func (s *LLMProviderService) UpdateProviderConfig(ctx context.Context, id int64, config domain.ProviderConfig) error {
	if err := s.repo.UpdateProviderConfig(ctx, id, config); err != nil {
		return err
	}
	// 캐시된 Provider 가 갱신 대상과 동일하면 무효화. 동일성 비교는 ID 로 한정.
	if cached := s.cache.Get(); cached != nil && cached.ID == id {
		s.cache.Invalidate()
	}
	return nil
}

// DeleteProvider 는 Provider 를 삭제한다. 캐시 무효화도 함께 수행한다.
// (안전을 위해 캐시된 Provider 가 동일 ID 인 경우 비운다.)
func (s *LLMProviderService) DeleteProvider(ctx context.Context, id int64) error {
	if err := s.repo.DeleteProvider(ctx, id); err != nil {
		return err
	}
	if cached := s.cache.Get(); cached != nil && cached.ID == id {
		s.cache.Invalidate()
	}
	return nil
}

// 컨벤션 유틸: 알려진 도메인 에러를 한 군데에서 분류하기 위한 헬퍼.
// 핸들러는 errors.Is 를 직접 사용하지만, 서비스 내부에서도 분기 가능.
//
//nolint:unused // 호출 측 확장용
func isDomainError(err error) bool {
	return errors.Is(err, domain.ErrProviderNotFound) ||
		errors.Is(err, domain.ErrNoActiveProvider) ||
		errors.Is(err, domain.ErrInvalidProvider) ||
		errors.Is(err, domain.ErrInvalidProviderType) ||
		errors.Is(err, domain.ErrProviderConflict)
}
