package db

import (
	"context"
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

	got, data, err := store.Get(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "doc.pdf", got.FileName)
	assert.Equal(t, "application/pdf", got.ContentType)
	assert.Equal(t, int64(5), got.SizeBytes)
	assert.Equal(t, "u1", got.UploaderID)
	assert.Equal(t, payload, data)

	_, _, err = store.Get(ctx, "nope")
	assert.ErrorIs(t, err, domainmessaging.ErrAttachmentNotFound)
}
