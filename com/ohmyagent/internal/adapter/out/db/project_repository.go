package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.ProjectRepository = (*ProjectRepository)(nil)

// ProjectRepository 는 프로젝트 메타데이터를 영속화한다(owner+client_id 업서트).
type ProjectRepository struct {
	db     *sql.DB
	driver string // mysql | sqlite (atomic upsert SQL 분기)
}

func NewProjectRepository(conn *sql.DB, driver string) *ProjectRepository {
	return &ProjectRepository{db: conn, driver: driver}
}

// UpsertProject 는 owner+client_id 로 프로젝트를 driver 별 atomic upsert 한다(동시 동기화 레이스·왕복 제거).
// created_utc 는 최초 INSERT 값을 유지하고 UPDATE 시 갱신하지 않는다.
func (r *ProjectRepository) UpsertProject(ctx context.Context, p domainproject.Project) (domainproject.Project, error) {
	tail := " ON CONFLICT(owner_id, client_id) DO UPDATE SET name=excluded.name, updated_utc=excluded.updated_utc" // sqlite
	if r.driver == "mysql" {
		tail = " ON DUPLICATE KEY UPDATE name=VALUES(name), updated_utc=VALUES(updated_utc)"
	}
	q := "INSERT INTO projects (id, owner_id, client_id, name, created_utc, updated_utc) VALUES (?,?,?,?,?,?)" + tail
	if _, err := r.db.ExecContext(ctx, q, p.ID, p.OwnerID, p.ClientID, p.Name, p.CreatedUTC.Unix(), p.UpdatedUTC.Unix()); err != nil {
		return domainproject.Project{}, fmt.Errorf("project: upsert: %w", err)
	}
	var createdUnix int64
	if err := r.db.QueryRowContext(ctx, "SELECT id, created_utc FROM projects WHERE owner_id=? AND client_id=?", p.OwnerID, p.ClientID).Scan(&p.ID, &createdUnix); err != nil {
		return domainproject.Project{}, fmt.Errorf("project: upsert reload: %w", err)
	}
	p.CreatedUTC = time.Unix(createdUnix, 0).UTC()
	return p, nil
}

func (r *ProjectRepository) ListProjects(ctx context.Context, ownerID string) ([]domainproject.Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, client_id, name, created_utc, updated_utc,
		(SELECT COUNT(*) FROM conversations c WHERE c.project_id = projects.id) AS cnt
		FROM projects WHERE owner_id=? ORDER BY updated_utc DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("project: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domainproject.Project
	for rows.Next() {
		p := domainproject.Project{OwnerID: ownerID}
		var createdUnix, updatedUnix int64
		if err := rows.Scan(&p.ID, &p.ClientID, &p.Name, &createdUnix, &updatedUnix, &p.ConversationCount); err != nil {
			return nil, fmt.Errorf("project: scan: %w", err)
		}
		p.CreatedUTC = time.Unix(createdUnix, 0).UTC()
		p.UpdatedUTC = time.Unix(updatedUnix, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *ProjectRepository) GetProject(ctx context.Context, ownerID, id string) (domainproject.Project, error) {
	p := domainproject.Project{OwnerID: ownerID}
	var createdUnix, updatedUnix int64
	err := r.db.QueryRowContext(ctx, `SELECT id, client_id, name, created_utc, updated_utc,
		(SELECT COUNT(*) FROM conversations c WHERE c.project_id = projects.id)
		FROM projects WHERE owner_id=? AND id=?`, ownerID, id).
		Scan(&p.ID, &p.ClientID, &p.Name, &createdUnix, &updatedUnix, &p.ConversationCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domainproject.Project{}, domainproject.ErrNotFound
	}
	if err != nil {
		return domainproject.Project{}, fmt.Errorf("project: get: %w", err)
	}
	p.CreatedUTC = time.Unix(createdUnix, 0).UTC()
	p.UpdatedUTC = time.Unix(updatedUnix, 0).UTC()
	return p, nil
}

// DeleteProject 는 프로젝트와 소속 대화 메타데이터를 한 트랜잭션에서 삭제한다(고아 행 방지, 본문 블롭은 보존).
func (r *ProjectRepository) DeleteProject(ctx context.Context, ownerID, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("project: delete begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE owner_id=? AND id=?", ownerID, id)
	if err != nil {
		return fmt.Errorf("project: delete: %w", err)
	}
	if err := affectedOrNotFound(res, domainproject.ErrNotFound); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM conversations WHERE owner_id=? AND project_id=?", ownerID, id); err != nil {
		return fmt.Errorf("project: cascade delete conversations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("project: delete commit: %w", err)
	}
	return nil
}
