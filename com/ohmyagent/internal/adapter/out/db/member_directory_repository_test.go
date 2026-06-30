package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// TestMemberDirectoryRepository 는 멤버 ID → 이름(username/display_name) 배치 해석을 검증한다.
func TestMemberDirectoryRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/md.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	var roleID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT id FROM roles ORDER BY id LIMIT 1").Scan(&roleID))

	members := NewMemberRepository(conn)
	now := time.Unix(1000, 0).UTC()
	require.NoError(t, members.Save(ctx, domainauth.Member{
		ID: "m1", Username: "admin", PasswordHash: "h", Active: true,
		Role: domainauth.Role{ID: roleID}, DisplayName: "신성현", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, members.Save(ctx, domainauth.Member{
		ID: "m2", Username: "probe2", PasswordHash: "h", Active: true,
		Role: domainauth.Role{ID: roleID}, CreatedAt: now, UpdatedAt: now, // display_name 없음
	}))

	dir := NewMemberDirectoryRepository(conn)

	// 빈 입력 → 빈 맵(쿼리 생략).
	empty, err := dir.NamesByIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	// 존재하는 id 만 해석, 없는 id(ghost)는 맵에서 제외.
	got, err := dir.NamesByIDs(ctx, []string{"m1", "m2", "ghost"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, "admin", got["m1"].Username)
	assert.Equal(t, "신성현", got["m1"].DisplayName)
	assert.Equal(t, "probe2", got["m2"].Username)
	assert.Empty(t, got["m2"].DisplayName)
	_, hasGhost := got["ghost"]
	assert.False(t, hasGhost)
}
