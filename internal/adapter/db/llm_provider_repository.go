package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"

	sqlcdb "OhMyAgent.AiAgent.Server/internal/adapter/db/sqlc"
)

// LLMProviderRepository 는 sqlc 생성 코드를 사용하여 port.LLMRepository 를 구현한다.
// 도메인 ↔ DB 모델 매핑이 모두 이 파일 안에서 수행되어, 도메인/포트 레이어가
// sqlc 패키지에 의존하지 않도록 한다.
type LLMProviderRepository struct {
	db      *sql.DB
	queries *sqlcdb.Queries
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.LLMRepository = (*LLMProviderRepository)(nil)

// NewLLMProviderRepository 는 *sql.DB 를 받아 레포지토리를 생성한다.
// *sql.DB 를 보관하는 이유는 ActivateProvider 트랜잭션을 직접 열어야 하기 때문이다.
func NewLLMProviderRepository(database *sql.DB) *LLMProviderRepository {
	return &LLMProviderRepository{
		db:      database,
		queries: sqlcdb.New(database),
	}
}

// ---------------------------------------------------------------------------
// 조회 계열
// ---------------------------------------------------------------------------

// GetActiveProvider 는 is_active=1 인 Provider 한 건을 반환한다.
// 활성 Provider 가 없으면 domain.ErrNoActiveProvider 로 변환한다.
func (r *LLMProviderRepository) GetActiveProvider(ctx context.Context) (*domain.LLMProvider, error) {
	row, err := r.queries.GetActiveProvider(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNoActiveProvider
		}
		return nil, fmt.Errorf("repo: get active provider: %w", err)
	}
	return toDomainProvider(row)
}

// GetProviderByID 는 단건 조회. 없으면 domain.ErrProviderNotFound.
func (r *LLMProviderRepository) GetProviderByID(ctx context.Context, id int64) (*domain.LLMProvider, error) {
	row, err := r.queries.GetProviderByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, fmt.Errorf("repo: get provider by id=%d: %w", id, err)
	}
	return toDomainProvider(row)
}

// ListProviders 는 전체 목록을 반환한다.
func (r *LLMProviderRepository) ListProviders(ctx context.Context) ([]*domain.LLMProvider, error) {
	rows, err := r.queries.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: list providers: %w", err)
	}
	out := make([]*domain.LLMProvider, 0, len(rows))
	for _, row := range rows {
		p, mErr := toDomainProvider(row)
		if mErr != nil {
			return nil, mErr
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 변경 계열
// ---------------------------------------------------------------------------

// CreateProvider 는 새 Provider 를 INSERT 한 후 채워진 도메인 엔티티를 반환한다.
func (r *LLMProviderRepository) CreateProvider(ctx context.Context, provider *domain.LLMProvider) (*domain.LLMProvider, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}

	configBytes, err := json.Marshal(provider.Config)
	if err != nil {
		return nil, fmt.Errorf("repo: marshal config: %w", err)
	}

	row, err := r.queries.CreateProvider(ctx, sqlcdb.CreateProviderParams{
		Name:         provider.Name,
		IsActive:     provider.IsActive,
		ProviderType: toSQLProviderType(provider.ProviderType),
		ConfigJson:   configBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("repo: create provider: %w", err)
	}
	return toDomainProvider(row)
}

// ActivateProvider 는 모든 Provider 를 비활성화한 뒤, 지정한 id 만 활성화한다.
// DeactivateAll → ActivateByID 두 단계를 단일 트랜잭션으로 묶어 원자성을 보장한다.
// ActivateByID 의 영향 행이 0이면 트랜잭션을 롤백하고 ErrProviderNotFound 를 반환한다.
func (r *LLMProviderRepository) ActivateProvider(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repo: begin tx: %w", err)
	}
	// 안전망 - 정상 경로에서는 명시적으로 Commit 한다.
	defer func() {
		_ = tx.Rollback()
	}()

	qtx := r.queries.WithTx(tx)

	if err := qtx.DeactivateAllProviders(ctx); err != nil {
		return fmt.Errorf("repo: deactivate all: %w", err)
	}

	affected, err := qtx.ActivateProviderByID(ctx, id)
	if err != nil {
		return fmt.Errorf("repo: activate id=%d: %w", id, err)
	}
	if affected == 0 {
		return domain.ErrProviderNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repo: commit activate tx: %w", err)
	}
	return nil
}

// UpdateProviderConfig 는 단일 Provider 의 config_json 만 갱신한다.
// 영향 행이 0이면 ErrProviderNotFound 를 반환한다.
func (r *LLMProviderRepository) UpdateProviderConfig(ctx context.Context, id int64, config domain.ProviderConfig) error {
	configBytes, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("repo: marshal config: %w", err)
	}
	affected, err := r.queries.UpdateProviderConfig(ctx, sqlcdb.UpdateProviderConfigParams{
		ConfigJson: configBytes,
		ID:         id,
	})
	if err != nil {
		return fmt.Errorf("repo: update provider config id=%d: %w", id, err)
	}
	if affected == 0 {
		return domain.ErrProviderNotFound
	}
	return nil
}

// DeleteProvider 는 ID 기준 삭제. 영향 행이 0이면 ErrProviderNotFound.
func (r *LLMProviderRepository) DeleteProvider(ctx context.Context, id int64) error {
	affected, err := r.queries.DeleteProvider(ctx, id)
	if err != nil {
		return fmt.Errorf("repo: delete provider id=%d: %w", id, err)
	}
	if affected == 0 {
		return domain.ErrProviderNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 매핑 헬퍼 (sqlc ↔ domain)
//
// 이 헬퍼들은 어댑터 패키지 안에만 머무른다. 도메인/포트는 sqlc 모델을 모른다.
// ---------------------------------------------------------------------------

// toDomainProvider 는 sqlcdb.LlmProvider → domain.LLMProvider 매핑.
func toDomainProvider(src *sqlcdb.LlmProvider) (*domain.LLMProvider, error) {
	if src == nil {
		return nil, domain.ErrProviderNotFound
	}
	cfg, err := unmarshalConfig(src.ConfigJson)
	if err != nil {
		return nil, fmt.Errorf("repo: decode config_json (id=%d): %w", src.ID, err)
	}
	return &domain.LLMProvider{
		ID:           src.ID,
		Name:         src.Name,
		IsActive:     src.IsActive,
		ProviderType: toDomainProviderType(src.ProviderType),
		Config:       cfg,
		CreatedAt:    src.CreatedAt,
		UpdatedAt:    src.UpdatedAt,
	}, nil
}

// toDomainProviderType 은 ENUM → 도메인 타입 매핑.
func toDomainProviderType(t sqlcdb.LlmProvidersProviderType) domain.ProviderType {
	switch t {
	case sqlcdb.LlmProvidersProviderTypeLOCAL:
		return domain.ProviderTypeLocal
	case sqlcdb.LlmProvidersProviderTypeEXTERNAL:
		return domain.ProviderTypeExternal
	default:
		return domain.ProviderType(string(t))
	}
}

// toSQLProviderType 은 도메인 타입 → ENUM 매핑.
func toSQLProviderType(t domain.ProviderType) sqlcdb.LlmProvidersProviderType {
	switch t {
	case domain.ProviderTypeLocal:
		return sqlcdb.LlmProvidersProviderTypeLOCAL
	case domain.ProviderTypeExternal:
		return sqlcdb.LlmProvidersProviderTypeEXTERNAL
	default:
		return sqlcdb.LlmProvidersProviderType(string(t))
	}
}

// unmarshalConfig 는 빈 페이로드를 허용하면서 ProviderConfig 로 디코드한다.
func unmarshalConfig(raw json.RawMessage) (domain.ProviderConfig, error) {
	var cfg domain.ProviderConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
