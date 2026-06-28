package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domainclientversion "aiagent/com/ohmyagent/internal/domain/clientversion"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainclientversion.SettingsRepository = (*ClientVersionRepository)(nil)

// ClientVersionRepository 는 클라이언트 버전 설정을 client_version_settings 단일 행(id=1)에 보관한다.
type ClientVersionRepository struct {
	db *sql.DB
}

// NewClientVersionRepository 는 ClientVersionRepository 를 생성한다.
func NewClientVersionRepository(conn *sql.DB) *ClientVersionRepository {
	return &ClientVersionRepository{db: conn}
}

const clientVersionCols = "latest, minimum_supported, download_url, notice, mandatory, updated_at, updated_by"

// Get 은 현재 설정을 반환한다(없으면 DefaultSettings).
func (r *ClientVersionRepository) Get(ctx context.Context) (domainclientversion.Settings, error) {
	var (
		s         domainclientversion.Settings
		updatedBy sql.NullString
	)
	err := r.db.QueryRowContext(ctx, "SELECT "+clientVersionCols+" FROM client_version_settings WHERE id=1").
		Scan(&s.Latest, &s.MinimumSupported, &s.DownloadURL, &s.Notice, &s.Mandatory, &s.UpdatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domainclientversion.DefaultSettings(), nil
	}
	if err != nil {
		return domainclientversion.Settings{}, fmt.Errorf("clientversion: get: %w", err)
	}
	s.UpdatedBy = updatedBy.String
	return s, nil
}

// Save 는 설정을 upsert(UPDATE id=1 → 없으면 INSERT) 한다.
func (r *ClientVersionRepository) Save(ctx context.Context, s domainclientversion.Settings) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE client_version_settings SET latest=?, minimum_supported=?, download_url=?, notice=?, mandatory=?, updated_at=?, updated_by=? WHERE id=1",
		s.Latest, s.MinimumSupported, s.DownloadURL, s.Notice, s.Mandatory, s.UpdatedAt, nullString(s.UpdatedBy))
	if err != nil {
		return fmt.Errorf("clientversion: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO client_version_settings (id, latest, minimum_supported, download_url, notice, mandatory, updated_at, updated_by) VALUES (1,?,?,?,?,?,?,?)",
		s.Latest, s.MinimumSupported, s.DownloadURL, s.Notice, s.Mandatory, s.UpdatedAt, nullString(s.UpdatedBy)); err != nil {
		return fmt.Errorf("clientversion: insert: %w", err)
	}
	return nil
}
