package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainagentregistry.KeyRepository = (*A2AKeyRepository)(nil)

// A2AKeyRepository 는 A2A 브로커 서명 키를 영속화한다.
// PrivateKeyPEM 은 유스케이스가 암호화한 값을 그대로 저장한다(이 레포는 암복호화하지 않음).
type A2AKeyRepository struct {
	db *sql.DB
}

// NewA2AKeyRepository 는 A2AKeyRepository 를 생성한다.
func NewA2AKeyRepository(conn *sql.DB) *A2AKeyRepository {
	return &A2AKeyRepository{db: conn}
}

// GetActive 는 활성 키 1개를 반환한다(v1 단일 활성 키 — 최신 생성 우선). 없으면 ErrNotFound.
func (r *A2AKeyRepository) GetActive(ctx context.Context) (domainagentregistry.A2AKey, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT kid, private_key_pem_encrypted, public_key_pem, active, created_at FROM a2a_keys WHERE active=? ORDER BY created_at DESC LIMIT 1", true)
	var k domainagentregistry.A2AKey
	var createdUnix int64
	err := row.Scan(&k.KID, &k.PrivateKeyPEM, &k.PublicKeyPEM, &k.Active, &createdUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return domainagentregistry.A2AKey{}, domainagentregistry.ErrNotFound
	}
	if err != nil {
		return domainagentregistry.A2AKey{}, fmt.Errorf("a2a key: get active: %w", err)
	}
	k.CreatedAt = time.Unix(createdUnix, 0).UTC()
	return k, nil
}

// Save 는 키를 저장한다(kid 는 불변 — 신규 행 INSERT 전용, 회전 시 새 kid 로 추가).
func (r *A2AKeyRepository) Save(ctx context.Context, k domainagentregistry.A2AKey) error {
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO a2a_keys (kid, private_key_pem_encrypted, public_key_pem, active, created_at) VALUES (?,?,?,?,?)",
		k.KID, k.PrivateKeyPEM, k.PublicKeyPEM, k.Active, k.CreatedAt.Unix(),
	); err != nil {
		return fmt.Errorf("a2a key: save: %w", err)
	}
	return nil
}
