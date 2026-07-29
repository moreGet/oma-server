package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var (
	_ domainproject.SettingsRepository    = (*SessionSettingsRepository)(nil)
	_ domainproject.MemberLimitRepository = (*MemberSessionLimitRepository)(nil)
)

const sessionSettingsCols = "backend, file_dir, s3_endpoint, s3_bucket, s3_region, s3_access_key, s3_secret_key, s3_use_ssl, default_max_sessions, updated_at, updated_by"

// SessionSettingsRepository 는 세션 저장 설정(단일 행 id=1)을 영속화한다.
type SessionSettingsRepository struct{ db *sql.DB }

func NewSessionSettingsRepository(conn *sql.DB) *SessionSettingsRepository {
	return &SessionSettingsRepository{db: conn}
}

func (r *SessionSettingsRepository) Get(ctx context.Context) (domainproject.Settings, error) {
	var (
		s                                             domainproject.Settings
		backend                                       string
		fileDir, s3ep, s3bucket, s3region, s3ak, s3sk sql.NullString
		updatedBy                                     sql.NullString
	)
	err := r.db.QueryRowContext(ctx, "SELECT "+sessionSettingsCols+" FROM session_settings WHERE id=1").
		Scan(&backend, &fileDir, &s3ep, &s3bucket, &s3region, &s3ak, &s3sk, &s.S3UseSSL, &s.DefaultMaxSessions, &s.UpdatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domainproject.DefaultSettings(), nil
	}
	if err != nil {
		return domainproject.Settings{}, fmt.Errorf("session settings: get: %w", err)
	}
	s.Backend = domainproject.Backend(backend)
	s.FileDir, s.S3Endpoint, s.S3Bucket = fileDir.String, s3ep.String, s3bucket.String
	s.S3Region, s.S3AccessKey, s.S3SecretKey = s3region.String, s3ak.String, s3sk.String
	s.UpdatedBy = updatedBy.String
	return s, nil
}

func (r *SessionSettingsRepository) Save(ctx context.Context, s domainproject.Settings) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE session_settings SET backend=?, file_dir=?, s3_endpoint=?, s3_bucket=?, s3_region=?,
		   s3_access_key=?, s3_secret_key=?, s3_use_ssl=?, default_max_sessions=?, updated_at=?, updated_by=? WHERE id=1`,
		string(s.Backend), nullString(s.FileDir), nullString(s.S3Endpoint), nullString(s.S3Bucket), nullString(s.S3Region),
		nullString(s.S3AccessKey), nullString(s.S3SecretKey), s.S3UseSSL, s.DefaultMaxSessions, s.UpdatedAt, nullString(s.UpdatedBy))
	if err != nil {
		return fmt.Errorf("session settings: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO session_settings (id, "+sessionSettingsCols+") VALUES (1,?,?,?,?,?,?,?,?,?,?,?)",
		string(s.Backend), nullString(s.FileDir), nullString(s.S3Endpoint), nullString(s.S3Bucket), nullString(s.S3Region),
		nullString(s.S3AccessKey), nullString(s.S3SecretKey), s.S3UseSSL, s.DefaultMaxSessions, s.UpdatedAt, nullString(s.UpdatedBy))
	if err != nil {
		return fmt.Errorf("session settings: insert: %w", err)
	}
	return nil
}

// MemberSessionLimitRepository 는 멤버별 최대 세션 수 오버라이드를 영속화한다.
type MemberSessionLimitRepository struct{ db *sql.DB }

func NewMemberSessionLimitRepository(conn *sql.DB) *MemberSessionLimitRepository {
	return &MemberSessionLimitRepository{db: conn}
}

func (r *MemberSessionLimitRepository) Get(ctx context.Context, memberID string) (int, error) {
	var max int
	err := r.db.QueryRowContext(ctx, "SELECT max_sessions FROM member_session_limits WHERE member_id=?", memberID).Scan(&max)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("session limit: get: %w", err)
	}
	return max, nil
}

func (r *MemberSessionLimitRepository) Set(ctx context.Context, memberID string, max int) error {
	res, err := r.db.ExecContext(ctx, "UPDATE member_session_limits SET max_sessions=? WHERE member_id=?", max, memberID)
	if err != nil {
		return fmt.Errorf("session limit: set: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO member_session_limits (member_id, max_sessions) VALUES (?,?)", memberID, max); err != nil {
		return fmt.Errorf("session limit: insert: %w", err)
	}
	return nil
}

// ByIDs 는 주어진 멤버들의 오버라이드만 조회한다(어드민 목록이 한 페이지분만 필요할 때).
// 결과 크기가 테이블 전체가 아니라 요청한 id 수에 묶인다.
func (r *MemberSessionLimitRepository) ByIDs(ctx context.Context, memberIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(memberIDs))
	if len(memberIDs) == 0 {
		return out, nil
	}
	ph, args := inPlaceholders(memberIDs)
	q := "SELECT member_id, max_sessions FROM member_session_limits WHERE max_sessions > 0 AND member_id IN (" +
		ph + ")"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session limit: by ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var m int
		if err := rows.Scan(&id, &m); err != nil {
			return nil, fmt.Errorf("session limit: scan: %w", err)
		}
		out[id] = m
	}
	return out, rows.Err()
}
