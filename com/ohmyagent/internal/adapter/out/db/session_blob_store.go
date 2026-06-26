package db

import (
	"context"
	"database/sql"
	"fmt"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.ContentStore = (*SessionBlobStore)(nil)

// SessionBlobStore 는 대화 본문을 DB(session_blobs)에 key 기반 upsert 로 저장한다(DB 백엔드).
type SessionBlobStore struct{ db *sql.DB }

func NewSessionBlobStore(conn *sql.DB) *SessionBlobStore { return &SessionBlobStore{db: conn} }

func (r *SessionBlobStore) Save(ctx context.Context, key string, data []byte) error {
	res, err := r.db.ExecContext(ctx, "UPDATE session_blobs SET content=? WHERE blob_key=?", data, key)
	if err != nil {
		return fmt.Errorf("session blob: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO session_blobs (blob_key, content) VALUES (?,?)", key, data); err != nil {
		return fmt.Errorf("session blob: insert: %w", err)
	}
	return nil
}
