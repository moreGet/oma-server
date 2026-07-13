package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.ConversationRepository = (*ConversationRepository)(nil)

// ConversationRepository 는 대화 메타데이터를 영속화한다(본문은 ContentStore).
type ConversationRepository struct {
	db     *sql.DB
	driver string // mysql | sqlite (atomic upsert SQL 분기)
}

func NewConversationRepository(conn *sql.DB, driver string) *ConversationRepository {
	return &ConversationRepository{db: conn, driver: driver}
}

// UpsertConversation 은 owner+client_id 로 대화를 driver 별 atomic upsert 한다(동시 동기화 레이스·왕복 제거).
func (r *ConversationRepository) UpsertConversation(ctx context.Context, c domainproject.Conversation) (domainproject.Conversation, error) {
	tail := " ON CONFLICT(owner_id, client_id) DO UPDATE SET project_id=excluded.project_id, title=excluded.title, updated_utc=excluded.updated_utc, message_count=excluded.message_count" // sqlite
	if r.driver == "mysql" {
		tail = " ON DUPLICATE KEY UPDATE project_id=VALUES(project_id), title=VALUES(title), updated_utc=VALUES(updated_utc), message_count=VALUES(message_count)"
	}
	q := "INSERT INTO conversations (id, project_id, owner_id, client_id, title, created_utc, updated_utc, message_count) VALUES (?,?,?,?,?,?,?,?)" + tail
	if _, err := r.db.ExecContext(ctx, q, c.ID, nullString(c.ProjectID), c.OwnerID, c.ClientID, c.Title, c.CreatedUTC.Unix(), c.UpdatedUTC.Unix(), c.MessageCount); err != nil {
		return domainproject.Conversation{}, fmt.Errorf("conversation: upsert: %w", err)
	}
	var createdUnix int64
	if err := r.db.QueryRowContext(ctx, "SELECT id, created_utc FROM conversations WHERE owner_id=? AND client_id=?", c.OwnerID, c.ClientID).Scan(&c.ID, &createdUnix); err != nil {
		return domainproject.Conversation{}, fmt.Errorf("conversation: upsert reload: %w", err)
	}
	c.CreatedUTC = time.Unix(createdUnix, 0).UTC()
	return c, nil
}

func (r *ConversationRepository) ListByProject(ctx context.Context, ownerID, projectID string) ([]domainproject.Conversation, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, client_id, title, created_utc, updated_utc, message_count FROM conversations WHERE owner_id=? AND project_id=? ORDER BY updated_utc DESC", ownerID, projectID)
	if err != nil {
		return nil, fmt.Errorf("conversation: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domainproject.Conversation
	for rows.Next() {
		c := domainproject.Conversation{OwnerID: ownerID, ProjectID: projectID}
		var createdUnix, updatedUnix int64
		if err := rows.Scan(&c.ID, &c.ClientID, &c.Title, &createdUnix, &updatedUnix, &c.MessageCount); err != nil {
			return nil, fmt.Errorf("conversation: scan: %w", err)
		}
		c.CreatedUTC = time.Unix(createdUnix, 0).UTC()
		c.UpdatedUTC = time.Unix(updatedUnix, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *ConversationRepository) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE owner_id=?", ownerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("conversation: count: %w", err)
	}
	return n, nil
}

func (r *ConversationRepository) FindIDByClient(ctx context.Context, ownerID, clientID string) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, "SELECT id FROM conversations WHERE owner_id=? AND client_id=?", ownerID, clientID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("conversation: find: %w", err)
	}
	return id, true, nil
}

func (r *ConversationRepository) DeleteConversation(ctx context.Context, ownerID, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM conversations WHERE owner_id=? AND id=?", ownerID, id)
	if err != nil {
		return fmt.Errorf("conversation: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domainproject.ErrNotFound
	}
	return nil
}
