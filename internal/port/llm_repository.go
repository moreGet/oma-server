package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// LLMRepository 는 LLM Provider 메타데이터의 영속화 계약이다.
// 구현체는 internal/adapter/db 에 위치하며, sqlc 생성 코드를 도메인 엔티티로 매핑한다.
type LLMRepository interface {
	// GetActiveProvider 는 is_active=1 인 Provider 를 반환한다.
	// 활성 Provider 가 없으면 domain.ErrNoActiveProvider 를 반환한다.
	GetActiveProvider(ctx context.Context) (*domain.LLMProvider, error)

	// GetProviderByID 는 단일 Provider 를 조회한다.
	// 대상이 없으면 domain.ErrProviderNotFound 를 반환한다.
	GetProviderByID(ctx context.Context, id int64) (*domain.LLMProvider, error)

	// ListProviders 는 전체 Provider 목록을 반환한다 (관리자 화면용).
	ListProviders(ctx context.Context) ([]*domain.LLMProvider, error)

	// CreateProvider 는 새 Provider 를 등록하고, 채워진 도메인 엔티티(생성된 ID, 타임스탬프 포함)를 반환한다.
	CreateProvider(ctx context.Context, provider *domain.LLMProvider) (*domain.LLMProvider, error)

	// ActivateProvider 는 트랜잭션으로 모두 비활성화한 뒤 지정 ID 만 활성화한다.
	// 대상 id 가 존재하지 않으면 domain.ErrProviderNotFound 를 반환하며 트랜잭션은 롤백된다.
	ActivateProvider(ctx context.Context, id int64) error

	// UpdateProviderConfig 는 config_json 만 갱신한다.
	UpdateProviderConfig(ctx context.Context, id int64, config domain.ProviderConfig) error

	// DeleteProvider 는 Provider 를 삭제한다.
	DeleteProvider(ctx context.Context, id int64) error
}
