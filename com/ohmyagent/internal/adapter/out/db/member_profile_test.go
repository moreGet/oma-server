package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// TestMemberRepository_ProfileColumns 는 00009 마이그레이션(email/display_name/organization)의
// Save·Scan·UpdateProfile 라운드트립을 검증한다.
func TestMemberRepository_ProfileColumns(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/mp.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	var roleID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT id FROM roles ORDER BY id LIMIT 1").Scan(&roleID))

	repo := NewMemberRepository(conn)
	now := time.Unix(1000, 0).UTC()
	require.NoError(t, repo.Save(ctx, domainauth.Member{
		ID: "m1", Username: "alice", PasswordHash: "h", Active: true,
		Role: domainauth.Role{ID: roleID}, Email: "a@x.com", DisplayName: "Alice", Organization: "Team",
		CreatedAt: now, UpdatedAt: now,
	}))

	got, err := repo.FindByID(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, "a@x.com", got.Email)
	assert.Equal(t, "Alice", got.DisplayName)
	assert.Equal(t, "Team", got.Organization)

	// 변경.
	require.NoError(t, repo.UpdateProfile(ctx, "m1", "b@y.com", "Alice2", "Team2", now.Unix(), "admin"))
	got2, _ := repo.FindByID(ctx, "m1")
	assert.Equal(t, "b@y.com", got2.Email)
	assert.Equal(t, "Alice2", got2.DisplayName)
	assert.Equal(t, "Team2", got2.Organization)

	// 빈 값으로 초기화.
	require.NoError(t, repo.UpdateProfile(ctx, "m1", "", "", "", now.Unix(), "admin"))
	got3, _ := repo.FindByID(ctx, "m1")
	assert.Empty(t, got3.Email)
	assert.Empty(t, got3.DisplayName)
	assert.Empty(t, got3.Organization)

	// 없는 id → ErrNotFound.
	assert.ErrorIs(t, repo.UpdateProfile(ctx, "nope", "x", "x", "x", now.Unix(), "admin"), domainauth.ErrNotFound)
}
