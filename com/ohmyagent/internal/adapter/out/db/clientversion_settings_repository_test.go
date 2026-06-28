package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientVersionRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/cv.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewClientVersionRepository(conn)

	// 시드 기본값.
	s, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", s.Latest)
	assert.Equal(t, "1.0.0", s.MinimumSupported)

	// 저장 + 라운드트립.
	s.Latest, s.MinimumSupported, s.DownloadURL, s.Notice, s.Mandatory = "1.4.0", "1.2.0", "https://x/dl", "보안 업데이트", true
	s.UpdatedBy, s.UpdatedAt = "admin-1", 1700000000
	require.NoError(t, repo.Save(ctx, s))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.4.0", got.Latest)
	assert.Equal(t, "https://x/dl", got.DownloadURL)
	assert.Equal(t, "보안 업데이트", got.Notice)
	assert.True(t, got.Mandatory)
	assert.Equal(t, "admin-1", got.UpdatedBy)
}
