package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// newSARepo 는 sqlite in-memory(파일) + 00028 마이그레이션 적용한 레포를 만든다.
func newSARepo(t *testing.T) (*ServiceAccountRepository, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/sa.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))
	return NewServiceAccountRepository(conn), ctx
}

// TestServiceAccountRepository_AccountRoundTrip 는 계정 Save/Find/List + soft revoke 를 검증한다.
func TestServiceAccountRepository_AccountRoundTrip(t *testing.T) {
	repo, ctx := newSARepo(t)
	now := time.Unix(1_753_000_000, 0).UTC()

	acct := domainserviceaccount.ServiceAccount{
		ID: "sa-1", Name: "bot", Description: "desc", OwnerMemberID: "m-1",
		CreatedAt: now, UpdatedAt: now, CreatedBy: "admin-1",
	}
	require.NoError(t, repo.SaveAccount(ctx, acct))

	t.Run("FindAccountByID 왕복(zero RevokedAt 유지)", func(t *testing.T) {
		got, err := repo.FindAccountByID(ctx, "sa-1")
		require.NoError(t, err)
		assert.Equal(t, "bot", got.Name)
		assert.Equal(t, "m-1", got.OwnerMemberID)
		assert.Equal(t, "admin-1", got.CreatedBy)
		assert.Equal(t, now, got.CreatedAt)
		assert.True(t, got.RevokedAt.IsZero(), "revoked_at=0 ↔ zero-time")
		assert.False(t, got.Revoked())
	})

	t.Run("미존재 → ErrNotFound", func(t *testing.T) {
		_, err := repo.FindAccountByID(ctx, "nope")
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})

	t.Run("ListAccounts 는 활성만", func(t *testing.T) {
		list, err := repo.ListAccounts(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, "sa-1", list[0].ID)
	})

	t.Run("RevokeAccount soft revoke → List 에서 제외, Find 는 폐기표시로 조회", func(t *testing.T) {
		revokedAt := now.Add(time.Hour)
		require.NoError(t, repo.RevokeAccount(ctx, "sa-1", revokedAt))

		got, err := repo.FindAccountByID(ctx, "sa-1")
		require.NoError(t, err, "폐기돼도 단건 조회는 가능(soft delete)")
		assert.True(t, got.Revoked())
		assert.Equal(t, revokedAt, got.RevokedAt)

		list, err := repo.ListAccounts(ctx)
		require.NoError(t, err)
		assert.Empty(t, list, "폐기 계정은 목록 제외")
	})

	t.Run("이미 폐기 계정 재폐기 → 멱등 아님(ErrNotFound)", func(t *testing.T) {
		err := repo.RevokeAccount(ctx, "sa-1", now.Add(2*time.Hour))
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})

	t.Run("미존재 계정 폐기 → ErrNotFound", func(t *testing.T) {
		err := repo.RevokeAccount(ctx, "nope", now)
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})
}

// TestServiceAccountRepository_KeyRoundTrip 는 키 Save/List + 0 sentinel↔zero-time 왕복 +
// FindKeyForAuth(JOIN) + soft revoke + RevokeKeysByAccount/TouchKeyLastUsed 0행 무해를 검증한다.
func TestServiceAccountRepository_KeyRoundTrip(t *testing.T) {
	repo, ctx := newSARepo(t)
	now := time.Unix(1_753_000_000, 0).UTC()

	acct := domainserviceaccount.ServiceAccount{
		ID: "sa-1", Name: "bot", OwnerMemberID: "m-1", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.SaveAccount(ctx, acct))

	// 무기한 키(expires=0, last_used=0, revoked=0).
	unlimited := domainserviceaccount.ServiceAccountKey{
		ID: "k-unlim", AccountID: "sa-1", TokenHash: "hash-unlim", CreatedAt: now, CreatedBy: "admin-1",
	}
	require.NoError(t, repo.SaveKey(ctx, unlimited))

	// 만료 지정 키.
	exp := now.Add(90 * 24 * time.Hour)
	expiring := domainserviceaccount.ServiceAccountKey{
		ID: "k-exp", AccountID: "sa-1", TokenHash: "hash-exp", CreatedAt: now, ExpiresAt: exp,
	}
	require.NoError(t, repo.SaveKey(ctx, expiring))

	t.Run("ListKeysByAccount + 0 sentinel↔zero-time 왕복", func(t *testing.T) {
		keys, err := repo.ListKeysByAccount(ctx, "sa-1")
		require.NoError(t, err)
		require.Len(t, keys, 2)
		byID := map[string]domainserviceaccount.ServiceAccountKey{}
		for _, k := range keys {
			byID[k.ID] = k
		}
		u := byID["k-unlim"]
		assert.True(t, u.ExpiresAt.IsZero(), "expires_at=0 ↔ zero-time")
		assert.True(t, u.LastUsedAt.IsZero(), "last_used_at=0 ↔ zero-time")
		assert.True(t, u.RevokedAt.IsZero(), "revoked_at=0 ↔ zero-time")
		assert.Equal(t, "admin-1", u.CreatedBy)

		e := byID["k-exp"]
		assert.Equal(t, exp, e.ExpiresAt, "지정 만료 왕복")
	})

	t.Run("FindKeyForAuth JOIN → 키 + 소속 계정 동시 복원", func(t *testing.T) {
		k, a, err := repo.FindKeyForAuth(ctx, "hash-exp")
		require.NoError(t, err)
		assert.Equal(t, "k-exp", k.ID)
		assert.Equal(t, exp, k.ExpiresAt)
		assert.Equal(t, "sa-1", a.ID, "소속 계정 복원")
		assert.Equal(t, "bot", a.Name)
		assert.Equal(t, "m-1", a.OwnerMemberID)
	})

	t.Run("FindKeyForAuth 미존재 해시 → ErrKeyNotFound", func(t *testing.T) {
		_, _, err := repo.FindKeyForAuth(ctx, "no-such-hash")
		assert.ErrorIs(t, err, domainserviceaccount.ErrKeyNotFound)
	})

	t.Run("TouchKeyLastUsed 갱신 반영", func(t *testing.T) {
		touched := now.Add(5 * time.Minute)
		require.NoError(t, repo.TouchKeyLastUsed(ctx, "k-unlim", touched))
		k, _, err := repo.FindKeyForAuth(ctx, "hash-unlim")
		require.NoError(t, err)
		assert.Equal(t, touched, k.LastUsedAt)
	})

	t.Run("TouchKeyLastUsed 미존재 키 → 0행 무해", func(t *testing.T) {
		assert.NoError(t, repo.TouchKeyLastUsed(ctx, "ghost", now))
	})

	t.Run("RevokeKey soft revoke(sa_id 스코프)", func(t *testing.T) {
		revokedAt := now.Add(time.Hour)
		require.NoError(t, repo.RevokeKey(ctx, "sa-1", "k-exp", revokedAt))
		k, _, err := repo.FindKeyForAuth(ctx, "hash-exp")
		require.NoError(t, err)
		assert.True(t, k.Revoked())
		assert.Equal(t, revokedAt, k.RevokedAt)
	})

	t.Run("RevokeKey 이미 폐기 재폐기 → ErrKeyNotFound", func(t *testing.T) {
		err := repo.RevokeKey(ctx, "sa-1", "k-exp", now.Add(2*time.Hour))
		assert.ErrorIs(t, err, domainserviceaccount.ErrKeyNotFound)
	})

	t.Run("RevokeKey 타 계정 스코프 → ErrKeyNotFound", func(t *testing.T) {
		err := repo.RevokeKey(ctx, "sa-OTHER", "k-unlim", now)
		assert.ErrorIs(t, err, domainserviceaccount.ErrKeyNotFound)
	})

	t.Run("RevokeKeysByAccount 활성 키 일괄 폐기", func(t *testing.T) {
		require.NoError(t, repo.RevokeKeysByAccount(ctx, "sa-1", now.Add(3*time.Hour)))
		k, _, err := repo.FindKeyForAuth(ctx, "hash-unlim")
		require.NoError(t, err)
		assert.True(t, k.Revoked(), "남은 활성 키도 폐기됨")
	})

	t.Run("RevokeKeysByAccount 대상 없음(모두 폐기) → 0행 무해", func(t *testing.T) {
		assert.NoError(t, repo.RevokeKeysByAccount(ctx, "sa-1", now.Add(4*time.Hour)))
		assert.NoError(t, repo.RevokeKeysByAccount(ctx, "sa-nonexistent", now))
	})
}
