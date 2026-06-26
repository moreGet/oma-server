package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// TestTranscriptStore_SaveRoundTrip 는 마이그레이션 적용 + gzip BLOB 저장 + 역직렬화를
// 임시 sqlite DB 로 통합 검증한다(00005 마이그레이션·스키마·압축 라운드트립 포함).
func TestTranscriptStore_SaveRoundTrip(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/t.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	store := NewTranscriptStore(conn)
	in := domaintranscript.Transcript{
		ID:               "tr-1",
		MemberID:         "m-1",
		Source:           domaintranscript.SourceChat,
		Model:            "gpt-4o-mini",
		Request:          []byte(`[{"role":"user","content":"hi"}]`),
		Response:         "안녕하세요",
		PromptTokens:     5,
		CompletionTokens: 2,
		TotalTokens:      7,
		FinishReason:     "stop",
		CreatedAt:        time.Unix(1000, 0).UTC(),
	}
	require.NoError(t, store.Save(ctx, in))

	var (
		model   string
		pt, tt  int
		source  string
		content []byte
		created int64
	)
	row := conn.QueryRowContext(ctx,
		"SELECT model, prompt_tokens, total_tokens, source, content, created_at FROM chat_transcripts WHERE id=?", "tr-1")
	require.NoError(t, row.Scan(&model, &pt, &tt, &source, &content, &created))

	assert.Equal(t, "gpt-4o-mini", model)
	assert.Equal(t, 5, pt)
	assert.Equal(t, 7, tt)
	assert.Equal(t, "chat", source)
	assert.Equal(t, int64(1000), created)

	// content 는 gzip(JSON{request,response}) 이어야 한다.
	zr, err := gzip.NewReader(bytes.NewReader(content))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)

	var c transcriptContent // 동일 패키지(transcript_store.go) 타입
	require.NoError(t, json.Unmarshal(raw, &c))
	assert.Equal(t, "안녕하세요", c.Response)
	assert.JSONEq(t, `[{"role":"user","content":"hi"}]`, string(c.Request))
}

// TestTranscriptStore_Purge 는 보존 정책 삭제(cutoff 이전만 삭제)를 검증한다.
func TestTranscriptStore_Purge(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/p.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	store := NewTranscriptStore(conn)
	require.NoError(t, store.Save(ctx, domaintranscript.Transcript{ID: "old", Source: domaintranscript.SourceChat, CreatedAt: time.Unix(1000, 0)}))
	require.NoError(t, store.Save(ctx, domaintranscript.Transcript{ID: "new", Source: domaintranscript.SourceChat, CreatedAt: time.Unix(2_000_000_000, 0)}))

	deleted, err := store.Purge(ctx, time.Unix(1_000_000, 0)) // 1_000_000 이전만 삭제 → old 만
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	var cnt int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_transcripts").Scan(&cnt))
	assert.Equal(t, 1, cnt) // new 만 남음
}
