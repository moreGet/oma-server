// Package llmproviderapp 는 LLM Provider 관리 + 활성 어댑터 조회 유스케이스를 담는다.
// 캐시 우선 조회, DB 폴백, 어댑터 생성 조율, 무효화 트리거, 인가 게이트를 여기서 수행한다.
package llmproviderapp

import (
	"context"
	"time"

	"github.com/google/uuid"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Service = (*ProviderService)(nil)

// accessGate 는 auth 도메인을 직접 import 하지 않기 위한 소비자 측 최소 인터페이스다(스펙 §4.4).
// main.go 에서 authUC 를 주입한다.
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// ProviderService 는 domainllmprovider.Service 구현이다.
type ProviderService struct {
	repo    domainllmprovider.Repository
	cache   domainllmprovider.Cache
	factory domainllmprovider.Factory
	gate    accessGate
}

// NewProviderService 는 의존성을 주입받아 ProviderService 를 생성한다.
func NewProviderService(
	repo domainllmprovider.Repository,
	cache domainllmprovider.Cache,
	factory domainllmprovider.Factory,
	gate accessGate,
) *ProviderService {
	return &ProviderService{repo: repo, cache: cache, factory: factory, gate: gate}
}

func (s *ProviderService) now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// List 는 전체 Provider 목록을 반환한다(조회=user, 라우트에서 게이트).
func (s *ProviderService) List(ctx context.Context, actorID string) ([]domainllmprovider.LLMProvider, error) {
	return s.repo.List(ctx)
}

// Get 은 ID 로 단일 Provider 를 조회한다.
func (s *ProviderService) Get(ctx context.Context, actorID, id string) (domainllmprovider.LLMProvider, error) {
	return s.repo.FindByID(ctx, id)
}

// Create 는 새 Provider 를 등록한다(admin↑).
func (s *ProviderService) Create(ctx context.Context, cmd domainllmprovider.CreateCommand) (domainllmprovider.LLMProvider, error) {
	if err := cmd.Validate(); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	now := s.now()
	p := domainllmprovider.LLMProvider{
		ID:           uuid.NewString(),
		Name:         cmd.Name,
		IsActive:     cmd.IsActive,
		ProviderType: cmd.ProviderType,
		Config:       cmd.Config,
		CreatedAt:    now,
		UpdatedAt:    now,
		CreatedBy:    cmd.ActorID,
		UpdatedBy:    cmd.ActorID,
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	// 활성 상태로 생성되었다면 캐시 무효화(다음 조회에서 lazy-load).
	if p.IsActive {
		s.cache.Invalidate()
	}
	return p, nil
}

// UpdateConfig 는 config 를 갱신한다(admin↑). 갱신 후 캐시를 무효화한다.
func (s *ProviderService) UpdateConfig(ctx context.Context, cmd domainllmprovider.UpdateConfigCommand) (domainllmprovider.LLMProvider, error) {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	updatedAt := s.now().Unix()
	if err := s.repo.UpdateConfig(ctx, cmd.ID, cmd.Config, updatedAt, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	s.cache.Invalidate()
	return s.repo.FindByID(ctx, cmd.ID)
}

// Activate 는 지정 ID 를 활성화하고(트랜잭션) 캐시를 무효화한다(admin↑).
func (s *ProviderService) Activate(ctx context.Context, cmd domainllmprovider.ActivateCommand) error {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	now := s.now().Unix()
	if err := s.repo.Activate(ctx, cmd.ID, now, cmd.ActorID); err != nil {
		return err
	}
	s.cache.Invalidate()
	return nil
}

// Delete 는 Provider 를 삭제하고 캐시를 무효화한다(admin↑).
func (s *ProviderService) Delete(ctx context.Context, cmd domainllmprovider.DeleteCommand) error {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, cmd.ID); err != nil {
		return err
	}
	s.cache.Invalidate()
	return nil
}

// GetActiveAdapter 는 캐시 → DB 폴백 → 팩토리 순으로 활성 Provider 의 어댑터를 반환한다.
func (s *ProviderService) GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error) {
	provider, ok := s.cache.Get()
	if !ok {
		p, err := s.repo.GetActive(ctx)
		if err != nil {
			return nil, err
		}
		s.cache.Set(p)
		provider = p
	}
	return s.factory.CreateAdapter(provider)
}
