package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// TestMemberToolPolicyRepository 는 00021 마이그레이션 + JSON 라운드트립 + Delete/All 을 검증한다.
func TestMemberToolPolicyRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/mtp.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewMemberToolPolicyRepository(conn)

	// 행이 없으면 빈 정책(MemberID 채움).
	p, err := repo.Get(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, "m1", p.MemberID)
	assert.Empty(t, p.Enabled)
	assert.Empty(t, p.Disabled)

	// 저장 + 라운드트립.
	p.Enabled = []string{"read_file"}
	p.Disabled = []string{"kill_process", "screenshot"}
	p.UpdatedBy = "admin-1"
	p.UpdatedAt = 1700000000
	require.NoError(t, repo.Save(ctx, p))

	got, err := repo.Get(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"read_file"}, got.Enabled)
	assert.Equal(t, []string{"kill_process", "screenshot"}, got.Disabled)
	assert.Equal(t, "admin-1", got.UpdatedBy)
	assert.Equal(t, int64(1700000000), got.UpdatedAt)

	// 두 번째 멤버 + All 스냅샷.
	require.NoError(t, repo.Save(ctx, domaintoolpolicy.MemberPolicy{MemberID: "m2", Enabled: []string{"grep"}}))
	all, err := repo.All(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Delete → 빈 정책으로 복원 + All 에서 제외.
	require.NoError(t, repo.Delete(ctx, "m1"))
	got, err = repo.Get(ctx, "m1")
	require.NoError(t, err)
	assert.Empty(t, got.Enabled)
	assert.Empty(t, got.Disabled)

	all, err = repo.All(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "m2", all[0].MemberID)
}
