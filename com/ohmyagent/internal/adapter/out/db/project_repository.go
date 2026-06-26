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
type ProjectRepository struct{ db *sql.DB }

func NewProjectRepository(conn *sql.DB) *ProjectRepository { return &ProjectRepository{db: conn} }

func (r *ProjectRepository) UpsertProject(ctx context.Context, p domainproject.Project) (domainproject.Project, error) {
	var existingID string
	var createdUnix int64
	err := r.db.QueryRowContext(ctx, "SELECT id, created_utc FROM projects WHERE owner_id=? AND client_id=?", p.OwnerID, p.ClientID).Scan(&existingID, &createdUnix)
	switch {
	case err == nil:
		if _, err := r.db.ExecContext(ctx, "UPDATE projects SET name=?, updated_utc=? WHERE id=?", p.Name, p.UpdatedUTC.Unix(), existingID); err != nil {
			return domainproject.Project{}, fmt.Errorf("project: update: %w", err)
		}
		p.ID = existingID
		p.CreatedUTC = time.Unix(createdUnix, 0).UTC()
		return p, nil
	case errors.Is(err, sql.ErrNoRows):
		if _, err := r.db.ExecContext(ctx, "INSERT INTO projects (id, owner_id, client_id, name, created_utc, updated_utc) VALUES (?,?,?,?,?,?)",
			p.ID, p.OwnerID, p.ClientID, p.Name, p.CreatedUTC.Unix(), p.UpdatedUTC.Unix()); err != nil {
			return domainproject.Project{}, fmt.Errorf("project: insert: %w", err)
		}
		return p, nil
	default:
		return domainproject.Project{}, fmt.Errorf("project: lookup: %w", err)
	}
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

func (r *ProjectRepository) DeleteProject(ctx context.Context, ownerID, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM projects WHERE owner_id=? AND id=?", ownerID, id)
	if err != nil {
		return fmt.Errorf("project: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domainproject.ErrNotFound
	}
	// 소속 대화 메타데이터도 정리(본문 블롭은 보존 정책에 맡김).
	_, _ = r.db.ExecContext(ctx, "DELETE FROM conversations WHERE owner_id=? AND project_id=?", ownerID, id)
	return nil
}
