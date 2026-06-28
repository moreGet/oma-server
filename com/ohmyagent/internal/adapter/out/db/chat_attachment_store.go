package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainmessaging.AttachmentStore = (*ChatAttachmentStore)(nil)

// ChatAttachmentStore 는 채팅 첨부 바이너리를 chat_attachments 테이블(BLOB)에 저장한다.
// 향후 파일/S3 백엔드가 필요하면 AttachmentStore 를 다른 구현으로 교체하면 된다.
type ChatAttachmentStore struct {
	db *sql.DB
}

// NewChatAttachmentStore 는 ChatAttachmentStore 를 생성한다.
func NewChatAttachmentStore(conn *sql.DB) *ChatAttachmentStore {
	return &ChatAttachmentStore{db: conn}
}

// Put 은 첨부 메타데이터 + 바이너리를 저장한다.
func (s *ChatAttachmentStore) Put(ctx context.Context, a domainmessaging.StoredAttachment, data []byte) error {
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO chat_attachments (id, file_name, content_type, size_bytes, uploader_id, created_at, data) VALUES (?,?,?,?,?,?,?)",
		a.ID, a.FileName, a.ContentType, a.SizeBytes, nullString(a.UploaderID), a.CreatedAt, data,
	); err != nil {
		return fmt.Errorf("attachment: put: %w", err)
	}
	return nil
}

// Get 은 첨부 메타데이터 + 바이너리를 반환한다(없으면 ErrAttachmentNotFound).
func (s *ChatAttachmentStore) Get(ctx context.Context, id string) (domainmessaging.StoredAttachment, []byte, error) {
	var (
		a        domainmessaging.StoredAttachment
		uploader sql.NullString
		data     []byte
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT id, file_name, content_type, size_bytes, uploader_id, created_at, data FROM chat_attachments WHERE id=?", id).
		Scan(&a.ID, &a.FileName, &a.ContentType, &a.SizeBytes, &uploader, &a.CreatedAt, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmessaging.StoredAttachment{}, nil, domainmessaging.ErrAttachmentNotFound
	}
	if err != nil {
		return domainmessaging.StoredAttachment{}, nil, fmt.Errorf("attachment: get: %w", err)
	}
	a.UploaderID = uploader.String
	return a, data, nil
}

// Stats 는 첨부 개수와 총 바이트 수를 반환한다(어드민 집계).
func (s *ChatAttachmentStore) Stats(ctx context.Context) (int, int64, error) {
	var (
		count int
		bytes int64
	)
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM chat_attachments").Scan(&count, &bytes); err != nil {
		return 0, 0, fmt.Errorf("attachment: stats: %w", err)
	}
	return count, bytes, nil
}
