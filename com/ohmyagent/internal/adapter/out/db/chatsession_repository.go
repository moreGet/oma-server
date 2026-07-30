package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainchatsession "aiagent/com/ohmyagent/internal/domain/chatsession"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainchatsession.Repository = (*ChatSessionRepository)(nil)

// ChatSessionRepository 는 chat_sessions 영속화를 담당한다(손작성).
type ChatSessionRepository struct {
	db *sql.DB
}

// NewChatSessionRepository 는 *sql.DB 를 받아 레포지토리를 생성한다.
func NewChatSessionRepository(conn *sql.DB) *ChatSessionRepository {
	return &ChatSessionRepository{db: conn}
}

const sessionColumns = "id, owner_id, title, data_json, created_at, updated_at"

// scanSessionSummary 는 한 행을 세션 요약으로 스캔한다(unix 초 → UTC 시각).
func scanSessionSummary(sc rowScanner) (domainchatsession.Summary, error) {
	var (
		s                    domainchatsession.Summary
		createdAt, updatedAt int64
	)
	if err := sc.Scan(&s.ID, &s.Title, &createdAt, &updatedAt); err != nil {
		return domainchatsession.Summary{}, err
	}
	s.CreatedAt = time.Unix(createdAt, 0).UTC()
	s.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return s, nil
}

// ListByOwner 는 소유자의 세션 요약 목록을 최신순으로 반환한다.
func (r *ChatSessionRepository) ListByOwner(ctx context.Context, ownerID string) ([]domainchatsession.Summary, error) {
	return queryList(ctx, r.db, "list sessions", scanSessionSummary,
		"SELECT id, title, created_at, updated_at FROM chat_sessions WHERE owner_id=? ORDER BY updated_at DESC", ownerID)
}

// Get 은 소유자 스코프 단건 조회. 없거나 타인 소유면 ErrNotFound.
func (r *ChatSessionRepository) Get(ctx context.Context, ownerID, id string) (domainchatsession.Session, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM chat_sessions WHERE id=? AND owner_id=?", id, ownerID)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainchatsession.Session{}, domainchatsession.ErrNotFound
	}
	if err != nil {
		return domainchatsession.Session{}, fmt.Errorf("get session id=%s: %w", id, err)
	}
	return s, nil
}

// FindOwner 는 ID 의 소유자를 반환한다(소유권 검증용). 없으면 ErrNotFound.
func (r *ChatSessionRepository) FindOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := r.db.QueryRowContext(ctx, "SELECT owner_id FROM chat_sessions WHERE id=?", id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domainchatsession.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find session owner id=%s: %w", id, err)
	}
	return owner, nil
}

// Insert 는 새 세션을 INSERT 한다.
func (r *ChatSessionRepository) Insert(ctx context.Context, s domainchatsession.Session) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO chat_sessions ("+sessionColumns+") VALUES (?,?,?,?,?,?)",
		s.ID, s.OwnerID, s.Title, string(s.Data), s.CreatedAt.Unix(), s.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Update 는 title/data/updated_at 을 갱신한다(소유자 스코프). 0행 → ErrNotFound.
func (r *ChatSessionRepository) Update(ctx context.Context, s domainchatsession.Session) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE chat_sessions SET title=?, data_json=?, updated_at=? WHERE id=? AND owner_id=?",
		s.Title, string(s.Data), s.UpdatedAt.Unix(), s.ID, s.OwnerID,
	)
	if err != nil {
		return fmt.Errorf("update session id=%s: %w", s.ID, err)
	}
	return affectedOrNotFound(res, domainchatsession.ErrNotFound)
}

// Delete 는 소유자 스코프 삭제. 0행 → ErrNotFound.
func (r *ChatSessionRepository) Delete(ctx context.Context, ownerID, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM chat_sessions WHERE id=? AND owner_id=?", id, ownerID)
	if err != nil {
		return fmt.Errorf("delete session id=%s: %w", id, err)
	}
	return affectedOrNotFound(res, domainchatsession.ErrNotFound)
}

// scanSession 은 한 행을 domainchatsession.Session 으로 스캔한다(sessionColumns 순서와 일치).
func scanSession(s rowScanner) (domainchatsession.Session, error) {
	var (
		sess      domainchatsession.Session
		dataJSON  string
		createdAt int64
		updatedAt int64
	)
	if err := s.Scan(&sess.ID, &sess.OwnerID, &sess.Title, &dataJSON, &createdAt, &updatedAt); err != nil {
		return domainchatsession.Session{}, err
	}
	sess.Data = []byte(dataJSON)
	sess.CreatedAt = time.Unix(createdAt, 0).UTC()
	sess.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return sess, nil
}
