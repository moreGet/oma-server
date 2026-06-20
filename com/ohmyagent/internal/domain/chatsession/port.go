package chatsession

import "context"

// Service — in 포트(유스케이스가 노출하는 능력). 모든 작업은 actor 소유 세션으로 한정된다.
type Service interface {
	List(ctx context.Context, actorID string) ([]Summary, error)
	Get(ctx context.Context, actorID, id string) (Session, error)   // 없거나 타인 소유면 ErrNotFound
	Upsert(ctx context.Context, cmd UpsertCommand) (Session, error) // 타인 소유 ID면 ErrPermission
	Delete(ctx context.Context, actorID, id string) error           // 없으면 ErrNotFound
}

// Repository — out 포트(영속화). 소유권은 owner_id 스코프 쿼리로 강제한다.
type Repository interface {
	ListByOwner(ctx context.Context, ownerID string) ([]Summary, error)
	Get(ctx context.Context, ownerID, id string) (Session, error) // 0행 → ErrNotFound
	FindOwner(ctx context.Context, id string) (string, error)     // 존재 시 owner_id, 없으면 ErrNotFound
	Insert(ctx context.Context, s Session) error
	Update(ctx context.Context, s Session) error          // 0행 → ErrNotFound
	Delete(ctx context.Context, ownerID, id string) error // 0행 → ErrNotFound
}
