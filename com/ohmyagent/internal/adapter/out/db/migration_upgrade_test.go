package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrationUpgrade_MixedState 는 in-place 마이그레이션 편집으로 생긴 "혼합 상태" DB
// (쿼터 일/주 컬럼 부재 + 대화이력 retention/strip 컬럼 존재)를 재현해,
// 조건부 Go 마이그레이션(10/11)이 누락분만 추가하고 중복 컬럼 에러 없이 보강하는지 검증한다.
func TestMigrationUpgrade_MixedState(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/up.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	// 혼합 상태 모사: 쿼터 일/주 컬럼만 제거(대화이력 retention/strip 은 그대로 둠) + goose 10/11 회수.
	for _, stmt := range []string{
		"ALTER TABLE member_token_limits DROP COLUMN daily_limit",
		"ALTER TABLE member_token_limits DROP COLUMN weekly_limit",
		"ALTER TABLE quota_config DROP COLUMN default_daily_limit",
		"ALTER TABLE quota_config DROP COLUMN default_weekly_limit",
		"DELETE FROM goose_db_version WHERE version_id IN (10, 11)",
	} {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	// 재기동 = 재마이그레이션. 11 은 retention/strip 이 이미 있으므로 skip(중복 에러 X), 10 은 일/주 추가.
	require.NoError(t, RunMigrations(ctx, "sqlite", conn), "idempotent re-migration must not fail on existing columns")

	q := NewQuotaRepository(conn, "sqlite")
	if _, err := q.MemberLimits(ctx, "x"); err != nil {
		t.Fatalf("quota daily/weekly columns missing after upgrade: %v", err)
	}
	if _, err := q.DefaultLimits(ctx); err != nil {
		t.Fatalf("quota_config day/week defaults missing: %v", err)
	}
	if _, err := NewTranscriptSettingsRepository(conn).Get(ctx); err != nil {
		t.Fatalf("transcript settings read failed: %v", err)
	}

	// 한 번 더 실행해도 안전(완전 idempotent).
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))
}
