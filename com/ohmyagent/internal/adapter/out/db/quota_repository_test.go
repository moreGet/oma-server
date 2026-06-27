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
	require.NoError(t, repo.AddUsage(ctx, "m1", []string{"2026-06-27"}, 100))
	require.NoError(t, repo.AddUsage(ctx, "m1", []string{"2026-06-27"}, 50))
	u, err := repo.UsageForPeriods(ctx, "m1", []string{"2026-06-27"})
	require.NoError(t, err)
	assert.Equal(t, 150, u["2026-06-27"])

	// 멀티로우 배치 누적: 한 번의 호출로 여러 기간을 동시에 += (채팅 완료당 단일 왕복).
	require.NoError(t, repo.AddUsage(ctx, "mb", []string{"2026-06-27", "2026-W26", "2026-06"}, 20))
	require.NoError(t, repo.AddUsage(ctx, "mb", []string{"2026-06-27", "2026-W26", "2026-06"}, 5))
	mb, err := repo.UsageForPeriods(ctx, "mb", []string{"2026-06-27", "2026-W26", "2026-06"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"2026-06-27": 25, "2026-W26": 25, "2026-06": 25}, mb)

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

	// UsageForPeriods: 여러 기간을 단일 쿼리로 — 존재하는 기간만 맵에 담기고 없는 기간은 생략(=0).
	require.NoError(t, repo.AddUsage(ctx, "m1", []string{"2026-W26"}, 40))
	multi, err := repo.UsageForPeriods(ctx, "m1", []string{"2026-06-27", "2026-W26", "2026-06"})
	require.NoError(t, err)
	assert.Equal(t, 150, multi["2026-06-27"])
	assert.Equal(t, 40, multi["2026-W26"])
	_, ok := multi["2026-06"] // 미사용 기간은 키 없음
	assert.False(t, ok)
	// 빈 입력은 빈 맵(쿼리 생략).
	empty, err := repo.UsageForPeriods(ctx, "m1", nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
