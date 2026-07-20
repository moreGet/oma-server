package db

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// TestChatAttachmentStore 는 00017 마이그레이션 + BLOB 라운드트립 + 없는 id 를 검증한다.
func TestChatAttachmentStore(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/att.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	store := NewChatAttachmentStore(conn)
	payload := []byte{0x00, 0x01, 0x02, 0xff, 0xfe} // 바이너리(널 포함)

	require.NoError(t, store.Put(ctx, domainmessaging.StoredAttachment{
		ID: "a1", FileName: "doc.pdf", ContentType: "application/pdf",
		SizeBytes: int64(len(payload)), UploaderID: "u1", CreatedAt: 1000,
	}, payload))

	got, rc, err := store.Open(ctx, "a1")
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "doc.pdf", got.FileName)
	assert.Equal(t, "application/pdf", got.ContentType)
	assert.Equal(t, int64(5), got.SizeBytes)
	assert.Equal(t, "u1", got.UploaderID)
	assert.Equal(t, payload, data)

	_, _, err = store.Open(ctx, "nope")
	assert.ErrorIs(t, err, domainmessaging.ErrAttachmentNotFound)
}

// 청크 경계에서 바이트가 어긋나지 않는지 검증한다. attachmentChunkBytes 를 넘는 크기를
// 위치 의존 패턴으로 채워, 경계 오프셋(1-base substr) 실수를 잡아낸다.
func TestChatAttachmentStore_ChunkedReadCrossesBoundaries(t *testing.T) {
	ctx := context.Background()
	conn, err := Open("sqlite", "file:"+t.TempDir()+"/chunk.db?_pragma=busy_timeout(5000)", 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	store := NewChatAttachmentStore(conn)
	// 청크 2개 + 나머지 → 경계를 두 번 넘는다.
	payload := make([]byte, 2*attachmentChunkBytes+1234)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	require.NoError(t, store.Put(ctx, domainmessaging.StoredAttachment{
		ID: "big", FileName: "big.bin", ContentType: "application/octet-stream",
		SizeBytes: int64(len(payload)), CreatedAt: 1,
	}, payload))

	_, rc, err := store.Open(ctx, "big")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Len(t, got, len(payload))
	assert.True(t, bytes.Equal(payload, got), "청크 경계에서 바이트가 어긋나면 안 된다")
}

// size_bytes 가 실제 BLOB 보다 크면(데이터 불일치) 무한 루프 없이 EOF 로 끝나야 한다.
func TestChatAttachmentStore_SizeMismatchTerminates(t *testing.T) {
	ctx := context.Background()
	conn, err := Open("sqlite", "file:"+t.TempDir()+"/mismatch.db?_pragma=busy_timeout(5000)", 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	store := NewChatAttachmentStore(conn)
	payload := []byte("short")
	require.NoError(t, store.Put(ctx, domainmessaging.StoredAttachment{
		ID: "bad", FileName: "b", ContentType: "text/plain",
		SizeBytes: 1 << 20, // 실제보다 훨씬 큰 값
		CreatedAt: 1,
	}, payload))

	_, rc, err := store.Open(ctx, "bad")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}
