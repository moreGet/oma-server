package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// TestTranscriptSettingsRepository_DefaultAndUpsert 는 00006 마이그레이션의 기본 행 +
// upsert(UPDATE→INSERT) + 스캔 라운드트립을 임시 sqlite 로 검증한다.
func TestTranscriptSettingsRepository_DefaultAndUpsert(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/s.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewTranscriptSettingsRepository(conn)

	// 마이그레이션 기본 행(id=1, enabled, db).
	s, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.True(t, s.Enabled)
	assert.Equal(t, domaintranscript.BackendDB, s.Backend)

	// file 백엔드로 변경 저장(UPDATE 경로).
	s.Backend = domaintranscript.BackendFile
	s.FileDir = "/var/data/transcripts"
	s.UpdatedBy = "admin-1"
	s.UpdatedAt = 1234
	require.NoError(t, repo.Save(ctx, s))

	got, err := repo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, domaintranscript.BackendFile, got.Backend)
	assert.Equal(t, "/var/data/transcripts", got.FileDir)
	assert.Equal(t, "admin-1", got.UpdatedBy)
	assert.Equal(t, int64(1234), got.UpdatedAt)
}
