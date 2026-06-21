package llmprovider

import "context"

// Service — in 포트(유스케이스가 노출하는 능력).
type Service interface {
	List(ctx context.Context, actorID string) ([]LLMProvider, error)
	Get(ctx context.Context, actorID, id string) (LLMProvider, error)
	Create(ctx context.Context, cmd CreateCommand) (LLMProvider, error)
	UpdateConfig(ctx context.Context, cmd UpdateConfigCommand) (LLMProvider, error)
	Activate(ctx context.Context, cmd ActivateCommand) error
	Delete(ctx context.Context, cmd DeleteCommand) error
	TestConnection(ctx context.Context, actorID, id string) error // 지정 Provider 연결 테스트(admin↑)
	GetActiveAdapter(ctx context.Context) (Adapter, error)        // 캐시 → DB 폴백 → 팩토리
}

// Repository — out 포트(영속화). 손작성 repository(sqlc 제거).
type Repository interface {
	GetActive(ctx context.Context) (LLMProvider, error)           // 없으면 ErrNoActiveProvider
	FindByID(ctx context.Context, id string) (LLMProvider, error) // 없으면 ErrNotFound
	List(ctx context.Context) ([]LLMProvider, error)
	Save(ctx context.Context, p LLMProvider) error                                                            // INSERT
	UpdateConfig(ctx context.Context, id string, cfg ProviderConfig, updatedAt int64, updatedBy string) error // 0행 → ErrNotFound
	Activate(ctx context.Context, id string, now int64, actorID string) error                                 // tx: 전체 비활성 → 지정 활성, 0행 → ErrNotFound
	Delete(ctx context.Context, id string) error                                                              // 0행 → ErrNotFound
}

// Cache — out 포트(활성 Provider 캐시, atomic.Value 기반).
type Cache interface {
	Get() (LLMProvider, bool)
	Set(p LLMProvider)
	Invalidate()
}

// Factory — out 포트(Provider → Adapter 인스턴스화).
type Factory interface {
	CreateAdapter(p LLMProvider) (Adapter, error)
}

// Adapter — out 포트(LLM 벤더 한 인스턴스).
// ChatStream 은 onChunk 콜백으로 응답 조각을 순차 전달하며, 마지막에 Done=true 조각을 1회 보낸다.
// onChunk 가 에러를 반환하면(예: 클라이언트 연결 종료) 스트리밍을 중단하고 그 에러를 반환한다.
type Adapter interface {
	ChatStream(ctx context.Context, req ChatRequest, onChunk func(ChatStreamChunk) error) error
	ProviderType() ProviderType
}
