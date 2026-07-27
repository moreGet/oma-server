package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainserviceaccount.Repository = (*ServiceAccountRepository)(nil)

// ServiceAccountRepository 는 서비스 계정과 딸린 API 키를 영속화한다.
// 시각은 unix seconds(BIGINT)로 저장하고 0 sentinel ↔ zero-time 으로 변환한다.
// 폐기·만료 판정은 SQL where 로 하지 않고 행을 가져와 앱단 IsUsable 로 처리한다(driver 차이 회피).
type ServiceAccountRepository struct {
	db *sql.DB
}

// NewServiceAccountRepository 는 ServiceAccountRepository 를 생성한다.
func NewServiceAccountRepository(conn *sql.DB) *ServiceAccountRepository {
	return &ServiceAccountRepository{db: conn}
}

const serviceAccountColumns = "id, name, description, owner_member_id, created_at, updated_at, created_by, revoked_at"
const serviceAccountKeyColumns = "id, sa_id, token_hash, created_at, expires_at, last_used_at, revoked_at, created_by"

// JOIN 조회용 별칭 접두 컬럼(모호성 제거). scanKeyWithAccount 의 Scan 순서와 반드시 일치시킨다.
const serviceAccountKeyColumnsK = "k.id, k.sa_id, k.token_hash, k.created_at, k.expires_at, k.last_used_at, k.revoked_at, k.created_by"
const serviceAccountColumnsA = "a.id, a.name, a.description, a.owner_member_id, a.created_at, a.updated_at, a.created_by, a.revoked_at"

// SaveAccount 는 계정을 INSERT 한다.
func (r *ServiceAccountRepository) SaveAccount(ctx context.Context, a domainserviceaccount.ServiceAccount) error {
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO service_accounts ("+serviceAccountColumns+") VALUES (?,?,?,?,?,?,?,?)",
		a.ID, a.Name, a.Description, a.OwnerMemberID,
		a.CreatedAt.Unix(), a.UpdatedAt.Unix(), nullString(a.CreatedBy), unixOrZero(a.RevokedAt),
	); err != nil {
		return fmt.Errorf("service account: save account: %w", err)
	}
	return nil
}

// FindAccountByID 는 계정 단건 조회다(폐기 포함). 없으면 ErrNotFound.
func (r *ServiceAccountRepository) FindAccountByID(ctx context.Context, id string) (domainserviceaccount.ServiceAccount, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+serviceAccountColumns+" FROM service_accounts WHERE id=?", id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainserviceaccount.ServiceAccount{}, domainserviceaccount.ErrNotFound
	}
	if err != nil {
		return domainserviceaccount.ServiceAccount{}, fmt.Errorf("service account: find account: %w", err)
	}
	return a, nil
}

// ListAccounts 는 활성 계정만 조회한다(revoked_at=0). 생성 시각 내림차순.
func (r *ServiceAccountRepository) ListAccounts(ctx context.Context) ([]domainserviceaccount.ServiceAccount, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+serviceAccountColumns+" FROM service_accounts WHERE revoked_at=0 ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("service account: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domainserviceaccount.ServiceAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("service account: scan account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service account: rows: %w", err)
	}
	return out, nil
}

// RevokeAccount 는 계정을 soft 폐기한다(revoked_at=now). 0행 → ErrNotFound.
// 이미 폐기된 계정의 재폐기는 revoked_at 을 덮어쓰지 않도록 revoked_at=0 조건을 건다.
func (r *ServiceAccountRepository) RevokeAccount(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE service_accounts SET revoked_at=?, updated_at=? WHERE id=? AND revoked_at=0",
		now.Unix(), now.Unix(), id)
	if err != nil {
		return fmt.Errorf("service account: revoke account: %w", err)
	}
	return affectedOrNotFound(res, domainserviceaccount.ErrNotFound)
}

// SaveKey 는 API 키를 INSERT 한다.
func (r *ServiceAccountRepository) SaveKey(ctx context.Context, k domainserviceaccount.ServiceAccountKey) error {
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO service_account_keys ("+serviceAccountKeyColumns+") VALUES (?,?,?,?,?,?,?,?)",
		k.ID, k.AccountID, k.TokenHash, k.CreatedAt.Unix(),
		unixOrZero(k.ExpiresAt), unixOrZero(k.LastUsedAt), unixOrZero(k.RevokedAt), nullString(k.CreatedBy),
	); err != nil {
		return fmt.Errorf("service account: save key: %w", err)
	}
	return nil
}

// ListKeysByAccount 는 계정에 딸린 키 전부를 조회한다(폐기 포함). 생성 시각 내림차순.
func (r *ServiceAccountRepository) ListKeysByAccount(ctx context.Context, accountID string) ([]domainserviceaccount.ServiceAccountKey, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+serviceAccountKeyColumns+" FROM service_account_keys WHERE sa_id=? ORDER BY created_at DESC", accountID)
	if err != nil {
		return nil, fmt.Errorf("service account: list keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domainserviceaccount.ServiceAccountKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("service account: scan key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service account: rows: %w", err)
	}
	return out, nil
}

// RevokeKey 는 키를 soft 폐기한다(revoked_at=now). 0행 → ErrKeyNotFound.
// 이미 폐기된 키의 재폐기는 revoked_at 을 덮어쓰지 않도록 revoked_at=0 조건을 건다.
func (r *ServiceAccountRepository) RevokeKey(ctx context.Context, accountID, keyID string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE service_account_keys SET revoked_at=? WHERE id=? AND sa_id=? AND revoked_at=0",
		now.Unix(), keyID, accountID)
	if err != nil {
		return fmt.Errorf("service account: revoke key: %w", err)
	}
	return affectedOrNotFound(res, domainserviceaccount.ErrKeyNotFound)
}

// RevokeKeysByAccount 는 계정에 딸린 활성 키 전부를 soft 폐기한다(계정 폐기 시 연쇄).
// 폐기 대상 키가 없어도(모두 이미 폐기) 에러가 아니다.
func (r *ServiceAccountRepository) RevokeKeysByAccount(ctx context.Context, accountID string, now time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE service_account_keys SET revoked_at=? WHERE sa_id=? AND revoked_at=0",
		now.Unix(), accountID); err != nil {
		return fmt.Errorf("service account: revoke keys by account: %w", err)
	}
	return nil
}

// FindKeyForAuth 는 token_hash 로 (키 + 소속 계정)을 단일 JOIN 조회한다. 없으면 ErrKeyNotFound.
// 폐기·만료 판정은 여기서 하지 않는다 — 호출자가 IsUsable/Revoked 로 앱단 판정(driver 차이 회피).
func (r *ServiceAccountRepository) FindKeyForAuth(ctx context.Context, tokenHash string) (domainserviceaccount.ServiceAccountKey, domainserviceaccount.ServiceAccount, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+serviceAccountKeyColumnsK+", "+serviceAccountColumnsA+
			" FROM service_account_keys k JOIN service_accounts a ON a.id=k.sa_id WHERE k.token_hash=?", tokenHash)
	k, a, err := scanKeyWithAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainserviceaccount.ServiceAccountKey{}, domainserviceaccount.ServiceAccount{}, domainserviceaccount.ErrKeyNotFound
	}
	if err != nil {
		return domainserviceaccount.ServiceAccountKey{}, domainserviceaccount.ServiceAccount{}, fmt.Errorf("service account: find key for auth: %w", err)
	}
	return k, a, nil
}

// TouchKeyLastUsed 는 last_used_at 을 갱신한다(best-effort). 대상 행이 없어도 에러로 보지 않는다.
func (r *ServiceAccountRepository) TouchKeyLastUsed(ctx context.Context, keyID string, now time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE service_account_keys SET last_used_at=? WHERE id=?", now.Unix(), keyID); err != nil {
		return fmt.Errorf("service account: touch key last used: %w", err)
	}
	return nil
}

// --- 스캔 헬퍼 ---

// scanAccount 는 serviceAccountColumns 순서로 한 행을 도메인 엔티티로 복원한다.
func scanAccount(sc rowScanner) (domainserviceaccount.ServiceAccount, error) {
	var a domainserviceaccount.ServiceAccount
	var createdUnix, updatedUnix, revokedUnix int64
	var createdBy sql.NullString
	if err := sc.Scan(&a.ID, &a.Name, &a.Description, &a.OwnerMemberID,
		&createdUnix, &updatedUnix, &createdBy, &revokedUnix); err != nil {
		return domainserviceaccount.ServiceAccount{}, err
	}
	a.CreatedAt = time.Unix(createdUnix, 0).UTC()
	a.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	a.CreatedBy = strFromNull(createdBy)
	if revokedUnix > 0 {
		a.RevokedAt = time.Unix(revokedUnix, 0).UTC()
	}
	return a, nil
}

// scanKey 는 serviceAccountKeyColumns 순서로 한 행을 도메인 엔티티로 복원한다.
func scanKey(sc rowScanner) (domainserviceaccount.ServiceAccountKey, error) {
	var k domainserviceaccount.ServiceAccountKey
	var createdUnix, expiresUnix, lastUsedUnix, revokedUnix int64
	var createdBy sql.NullString
	if err := sc.Scan(&k.ID, &k.AccountID, &k.TokenHash,
		&createdUnix, &expiresUnix, &lastUsedUnix, &revokedUnix, &createdBy); err != nil {
		return domainserviceaccount.ServiceAccountKey{}, err
	}
	k.CreatedAt = time.Unix(createdUnix, 0).UTC()
	if expiresUnix > 0 {
		k.ExpiresAt = time.Unix(expiresUnix, 0).UTC()
	}
	if lastUsedUnix > 0 {
		k.LastUsedAt = time.Unix(lastUsedUnix, 0).UTC()
	}
	if revokedUnix > 0 {
		k.RevokedAt = time.Unix(revokedUnix, 0).UTC()
	}
	k.CreatedBy = strFromNull(createdBy)
	return k, nil
}

// scanKeyWithAccount 는 JOIN 결과(키 컬럼 + 계정 컬럼)를 한 번에 복원한다.
func scanKeyWithAccount(sc rowScanner) (domainserviceaccount.ServiceAccountKey, domainserviceaccount.ServiceAccount, error) {
	var k domainserviceaccount.ServiceAccountKey
	var a domainserviceaccount.ServiceAccount
	var kCreatedUnix, kExpiresUnix, kLastUsedUnix, kRevokedUnix int64
	var kCreatedBy sql.NullString
	var aCreatedUnix, aUpdatedUnix, aRevokedUnix int64
	var aCreatedBy sql.NullString
	if err := sc.Scan(
		&k.ID, &k.AccountID, &k.TokenHash, &kCreatedUnix, &kExpiresUnix, &kLastUsedUnix, &kRevokedUnix, &kCreatedBy,
		&a.ID, &a.Name, &a.Description, &a.OwnerMemberID, &aCreatedUnix, &aUpdatedUnix, &aCreatedBy, &aRevokedUnix,
	); err != nil {
		return domainserviceaccount.ServiceAccountKey{}, domainserviceaccount.ServiceAccount{}, err
	}
	k.CreatedAt = time.Unix(kCreatedUnix, 0).UTC()
	if kExpiresUnix > 0 {
		k.ExpiresAt = time.Unix(kExpiresUnix, 0).UTC()
	}
	if kLastUsedUnix > 0 {
		k.LastUsedAt = time.Unix(kLastUsedUnix, 0).UTC()
	}
	if kRevokedUnix > 0 {
		k.RevokedAt = time.Unix(kRevokedUnix, 0).UTC()
	}
	k.CreatedBy = strFromNull(kCreatedBy)
	a.CreatedAt = time.Unix(aCreatedUnix, 0).UTC()
	a.UpdatedAt = time.Unix(aUpdatedUnix, 0).UTC()
	a.CreatedBy = strFromNull(aCreatedBy)
	if aRevokedUnix > 0 {
		a.RevokedAt = time.Unix(aRevokedUnix, 0).UTC()
	}
	return k, a, nil
}

// unixOrZero 는 zero-time 을 0 sentinel 로, 그 외는 unix seconds 로 변환한다.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
