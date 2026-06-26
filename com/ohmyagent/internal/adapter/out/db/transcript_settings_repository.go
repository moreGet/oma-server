package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintranscript.SettingsRepository = (*TranscriptSettingsRepository)(nil)

// TranscriptSettingsRepository 는 transcript_settings 단일 행(id=1)을 관리한다.
type TranscriptSettingsRepository struct {
	db *sql.DB
}

// NewTranscriptSettingsRepository 는 TranscriptSettingsRepository 를 생성한다.
func NewTranscriptSettingsRepository(conn *sql.DB) *TranscriptSettingsRepository {
	return &TranscriptSettingsRepository{db: conn}
}

const transcriptSettingsCols = "enabled, backend, file_dir, s3_endpoint, s3_bucket, s3_region, s3_access_key, s3_secret_key, s3_use_ssl, retention_days, strip_attachments, updated_at, updated_by"

// Get 은 설정 단일 행을 조회한다. 행이 없으면 기본값을 반환한다.
func (r *TranscriptSettingsRepository) Get(ctx context.Context) (domaintranscript.Settings, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+transcriptSettingsCols+" FROM transcript_settings WHERE id=1")
	var (
		s         domaintranscript.Settings
		backend   string
		fileDir   sql.NullString
		s3ep      sql.NullString
		s3bucket  sql.NullString
		s3region  sql.NullString
		s3access  sql.NullString
		s3secret  sql.NullString
		updatedBy sql.NullString
	)
	err := row.Scan(&s.Enabled, &backend, &fileDir, &s3ep, &s3bucket, &s3region, &s3access, &s3secret, &s.S3UseSSL, &s.RetentionDays, &s.StripAttachments, &s.UpdatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintranscript.DefaultSettings(), nil
	}
	if err != nil {
		return domaintranscript.Settings{}, fmt.Errorf("transcript settings: get: %w", err)
	}
	s.Backend = domaintranscript.Backend(backend)
	s.FileDir = strFromNull(fileDir)
	s.S3Endpoint = strFromNull(s3ep)
	s.S3Bucket = strFromNull(s3bucket)
	s.S3Region = strFromNull(s3region)
	s.S3AccessKey = strFromNull(s3access)
	s.S3SecretKey = strFromNull(s3secret)
	s.UpdatedBy = strFromNull(updatedBy)
	return s, nil
}

// Save 는 설정을 upsert 한다(id=1). UPDATE 후 0행이면 INSERT(드라이버 무관 portable upsert).
func (r *TranscriptSettingsRepository) Save(ctx context.Context, s domaintranscript.Settings) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE transcript_settings SET
		   enabled=?, backend=?, file_dir=?, s3_endpoint=?, s3_bucket=?, s3_region=?,
		   s3_access_key=?, s3_secret_key=?, s3_use_ssl=?, retention_days=?, strip_attachments=?, updated_at=?, updated_by=?
		 WHERE id=1`,
		s.Enabled, string(s.Backend), nullString(s.FileDir), nullString(s.S3Endpoint), nullString(s.S3Bucket),
		nullString(s.S3Region), nullString(s.S3AccessKey), nullString(s.S3SecretKey), s.S3UseSSL, s.RetentionDays, s.StripAttachments, s.UpdatedAt, nullString(s.UpdatedBy),
	)
	if err != nil {
		return fmt.Errorf("transcript settings: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.db.ExecContext(ctx,
		"INSERT INTO transcript_settings (id, "+transcriptSettingsCols+") VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Enabled, string(s.Backend), nullString(s.FileDir), nullString(s.S3Endpoint), nullString(s.S3Bucket),
		nullString(s.S3Region), nullString(s.S3AccessKey), nullString(s.S3SecretKey), s.S3UseSSL, s.RetentionDays, s.StripAttachments, s.UpdatedAt, nullString(s.UpdatedBy),
	)
	if err != nil {
		return fmt.Errorf("transcript settings: insert: %w", err)
	}
	return nil
}
