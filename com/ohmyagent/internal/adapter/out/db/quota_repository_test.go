package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

// TestQuotaRepository 는 00007 마이그레이션 + 사용량 atomic 누적(upsert) + 일/주/월 한도 설정을 검증한다.
func TestQuotaRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/q.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewQuotaRepository(conn, "sqlite")

	// 사용량 누적(upsert) — 기간 키는 임의 문자열.
	require.NoError(t, repo.AddUsage(ctx, "m1", "2026-06-27", 100))
	require.NoError(t, repo.AddUsage(ctx, "m1", "2026-06-27", 50))
	used, err := repo.GetUsage(ctx, "m1", "2026-06-27")
	require.NoError(t, err)
	assert.Equal(t, 150, used)

	// 전역 기본 한도(시드 0 → 일/주/월 변경).
	def, _ := repo.DefaultLimits(ctx)
	assert.Equal(t, domainquota.Limits{}, def)
	require.NoError(t, repo.SetDefaultLimits(ctx, domainquota.Limits{Daily: 10, Weekly: 100, Monthly: 1000}))
	def, _ = repo.DefaultLimits(ctx)
	assert.Equal(t, domainquota.Limits{Daily: 10, Weekly: 100, Monthly: 1000}, def)

	// 멤버 한도 upsert(INSERT→UPDATE).
	ml, _ := repo.MemberLimits(ctx, "m1")
	assert.Equal(t, domainquota.Limits{}, ml)
	require.NoError(t, repo.SetMemberLimits(ctx, "m1", domainquota.Limits{Daily: 5}))
	require.NoError(t, repo.SetMemberLimits(ctx, "m1", domainquota.Limits{Daily: 7, Monthly: 700}))
	ml, _ = repo.MemberLimits(ctx, "m1")
	assert.Equal(t, domainquota.Limits{Daily: 7, Monthly: 700}, ml)

	// 스냅샷 맵.
	usageMap, _ := repo.UsageByPeriod(ctx, "2026-06-27")
	assert.Equal(t, 150, usageMap["m1"])
	limitsMap, _ := repo.AllMemberLimits(ctx)
	assert.Equal(t, domainquota.Limits{Daily: 7, Monthly: 700}, limitsMap["m1"])
}
