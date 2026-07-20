package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Repository = (*LLMProviderRepository)(nil)

// LLMProviderRepository 는 llm_providers 영속화를 담당한다(손작성, sqlc 제거).
// config_json(TEXT) ↔ domainllmprovider.ProviderConfig 변환을 json.Marshal/Unmarshal 로 수행한다.
type LLMProviderRepository struct {
	db *sql.DB
}

// NewLLMProviderRepository 는 *sql.DB 를 받아 레포지토리를 생성한다.
// *sql.DB 를 보관하는 이유는 Activate 트랜잭션을 직접 열어야 하기 때문이다.
func NewLLMProviderRepository(conn *sql.DB) *LLMProviderRepository {
	return &LLMProviderRepository{db: conn}
}

const providerColumns = "id, name, is_active, provider_type, config_json, created_at, updated_at, created_by, updated_by"

// GetActive 는 is_active=true 인 Provider 한 건을 반환한다. 없으면 ErrNoActiveProvider.
//
// 활성 Provider 는 원래 하나여야 한다(Activate 가 트랜잭션으로 보장). 다만 불변식이 깨져
// 여러 건이 활성인 상태에서 ORDER BY 가 없으면 DB 스캔 순서(대개 삽입 순)에 따라
// "가장 오래된" 행이 조용히 선택된다 — 방금 활성화한 Provider 를 두고 옛 Provider 로
// 요청이 나가는 형태로 드러난다. updated_at DESC 로 가장 최근에 활성화된 것을 고른다.
func (r *LLMProviderRepository) GetActive(ctx context.Context) (domainllmprovider.LLMProvider, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+providerColumns+" FROM llm_providers WHERE is_active=? ORDER BY updated_at DESC LIMIT 1", true)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainllmprovider.LLMProvider{}, domainllmprovider.ErrNoActiveProvider
	}
	if err != nil {
		return domainllmprovider.LLMProvider{}, fmt.Errorf("get active provider: %w", err)
	}
	return p, nil
}

// FindByID 는 단건 조회. 없으면 ErrNotFound.
func (r *LLMProviderRepository) FindByID(ctx context.Context, id string) (domainllmprovider.LLMProvider, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+providerColumns+" FROM llm_providers WHERE id=?", id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainllmprovider.LLMProvider{}, domainllmprovider.ErrNotFound
	}
	if err != nil {
		return domainllmprovider.LLMProvider{}, fmt.Errorf("find provider id=%s: %w", id, err)
	}
	return p, nil
}

// List 는 전체 Provider 목록을 반환한다.
func (r *LLMProviderRepository) List(ctx context.Context) ([]domainllmprovider.LLMProvider, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+providerColumns+" FROM llm_providers ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domainllmprovider.LLMProvider, 0)
	for rows.Next() {
		p, scanErr := scanProvider(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan provider: %w", scanErr)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate providers: %w", err)
	}
	return out, nil
}

// Save 는 새 Provider 를 INSERT 한다.
func (r *LLMProviderRepository) Save(ctx context.Context, p domainllmprovider.LLMProvider) error {
	configJSON, err := marshalConfig(p.Config)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		"INSERT INTO llm_providers ("+providerColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
		p.ID, p.Name, p.IsActive, string(p.ProviderType), configJSON,
		p.CreatedAt.Unix(), p.UpdatedAt.Unix(), nullString(p.CreatedBy), nullString(p.UpdatedBy),
	)
	if err != nil {
		return fmt.Errorf("save provider: %w", err)
	}
	return nil
}

// UpdateConfig 는 config_json + 감사필드만 갱신한다. 0행 → ErrNotFound.
func (r *LLMProviderRepository) UpdateConfig(ctx context.Context, id string, cfg domainllmprovider.ProviderConfig, updatedAt int64, updatedBy string) error {
	configJSON, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE llm_providers SET config_json=?, updated_at=?, updated_by=? WHERE id=?",
		configJSON, updatedAt, nullString(updatedBy), id,
	)
	if err != nil {
		return fmt.Errorf("update provider config id=%s: %w", id, err)
	}
	return checkProviderAffected(res)
}

// Activate 는 단일 트랜잭션으로 모두 비활성화한 뒤 지정 id 만 활성화한다.
// 대상 id 가 없으면 트랜잭션을 롤백하고 ErrNotFound 를 반환한다.
func (r *LLMProviderRepository) Activate(ctx context.Context, id string, now int64, actorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin activate tx: %w", err)
	}
	// 안전망 - 정상 경로에서는 명시적으로 Commit 한다.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"UPDATE llm_providers SET is_active=?, updated_at=?, updated_by=? WHERE is_active=?",
		false, now, nullString(actorID), true,
	); err != nil {
		return fmt.Errorf("deactivate all providers: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		"UPDATE llm_providers SET is_active=?, updated_at=?, updated_by=? WHERE id=?",
		true, now, nullString(actorID), id,
	)
	if err != nil {
		return fmt.Errorf("activate provider id=%s: %w", id, err)
	}
	if err := checkProviderAffected(res); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit activate tx: %w", err)
	}
	return nil
}

// Delete 는 ID 기준 삭제. 0행 → ErrNotFound.
func (r *LLMProviderRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM llm_providers WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete provider id=%s: %w", id, err)
	}
	return checkProviderAffected(res)
}

// scanProvider 는 한 행을 domainllmprovider.LLMProvider 로 스캔한다.
// providerColumns 의 순서와 정확히 일치해야 한다.
func scanProvider(s rowScanner) (domainllmprovider.LLMProvider, error) {
	var (
		p            domainllmprovider.LLMProvider
		providerType string
		configJSON   string
		createdAt    int64
		updatedAt    int64
		createdBy    sql.NullString
		updatedBy    sql.NullString
	)
	if err := s.Scan(
		&p.ID, &p.Name, &p.IsActive, &providerType, &configJSON,
		&createdAt, &updatedAt, &createdBy, &updatedBy,
	); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	cfg, err := unmarshalConfig(configJSON)
	if err != nil {
		return domainllmprovider.LLMProvider{}, fmt.Errorf("decode config_json (id=%s): %w", p.ID, err)
	}
	p.ProviderType = domainllmprovider.ProviderType(providerType)
	p.Config = cfg
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	p.CreatedBy = strFromNull(createdBy)
	p.UpdatedBy = strFromNull(updatedBy)
	return p, nil
}

// marshalConfig 는 ProviderConfig 를 config_json 문자열로 직렬화한다.
func marshalConfig(cfg domainllmprovider.ProviderConfig) (string, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(b), nil
}

// unmarshalConfig 는 빈 페이로드를 허용하면서 config_json 을 ProviderConfig 로 디코드한다.
func unmarshalConfig(raw string) (domainllmprovider.ProviderConfig, error) {
	var cfg domainllmprovider.ProviderConfig
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// checkProviderAffected 는 0행 영향을 ErrNotFound 로 변환한다(도메인별 별개 구현).
func checkProviderAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domainllmprovider.ErrNotFound
	}
	return nil
}
