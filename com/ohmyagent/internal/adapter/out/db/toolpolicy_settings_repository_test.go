package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// TestToolPolicyRepository 는 00012 마이그레이션(시드) + JSON 컬럼 라운드트립 + 빈 목록 복원을 검증한다.
func TestToolPolicyRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/tp.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewToolPolicyRepository(conn)

	// 시드 기본값(빈 정책 + cached).
	s, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "cached", s.Mode)
	assert.Empty(t, s.Enabled)
	assert.Empty(t, s.BlockedPatterns)

	// 저장 + 라운드트립.
	s.Mode = "realtime"
	s.Enabled = []string{"read_file", "grep"}
	s.Disabled = []string{"kill_process"}
	s.BlockedPatterns = []domaintoolpolicy.BlockedPattern{{Type: "regex", Pattern: `\bnet\s+user\b`, Reason: "계정", ScriptType: "powershell"}}
	s.BlockedPaths = []domaintoolpolicy.BlockedPath{{Type: "substring", Pattern: `D:\sensitive`, Reason: "민감"}}
	s.UpdatedBy = "admin-1"
	s.UpdatedAt = 1700000000
	require.NoError(t, repo.Save(ctx, s))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "realtime", got.Mode)
	assert.Equal(t, []string{"read_file", "grep"}, got.Enabled)
	assert.Equal(t, []string{"kill_process"}, got.Disabled)
	require.Len(t, got.BlockedPatterns, 1)
	assert.Equal(t, "powershell", got.BlockedPatterns[0].ScriptType)
	assert.Equal(t, `\bnet\s+user\b`, got.BlockedPatterns[0].Pattern)
	require.Len(t, got.BlockedPaths, 1)
	assert.Equal(t, `D:\sensitive`, got.BlockedPaths[0].Pattern)
	assert.Equal(t, "admin-1", got.UpdatedBy)
	assert.Equal(t, int64(1700000000), got.UpdatedAt)

	// 빈 목록 저장 → NULL/빈 문자열 → nil 로 복원.
	got.Enabled = nil
	got.BlockedPatterns = nil
	require.NoError(t, repo.Save(ctx, got))
	got2, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Empty(t, got2.Enabled)
	assert.Empty(t, got2.BlockedPatterns)
	assert.Equal(t, []string{"kill_process"}, got2.Disabled) // 나머지는 보존
}
