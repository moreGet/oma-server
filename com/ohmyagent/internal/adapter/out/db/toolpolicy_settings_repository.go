package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintoolpolicy.SettingsRepository = (*ToolPolicyRepository)(nil)

// ToolPolicyRepository 는 도구 정책을 tool_policy_settings 단일 행(id=1)에 보관한다.
// 가변 길이 목록(enabled/disabled/패턴/경로)은 JSON TEXT 컬럼으로 저장한다.
type ToolPolicyRepository struct {
	db *sql.DB
}

// NewToolPolicyRepository 는 ToolPolicyRepository 를 생성한다.
func NewToolPolicyRepository(conn *sql.DB) *ToolPolicyRepository {
	return &ToolPolicyRepository{db: conn}
}

const toolPolicyCols = "mode, enabled, disabled, blocked_patterns, blocked_paths, updated_at, updated_by"

// Get 은 현재 정책을 반환한다(행이 없으면 DefaultSettings).
func (r *ToolPolicyRepository) Get(ctx context.Context) (domaintoolpolicy.Settings, error) {
	var (
		mode                               string
		enabled, disabled, patterns, paths sql.NullString
		updatedAt                          int64
		updatedBy                          sql.NullString
	)
	err := r.db.QueryRowContext(ctx, "SELECT "+toolPolicyCols+" FROM tool_policy_settings WHERE id=1").
		Scan(&mode, &enabled, &disabled, &patterns, &paths, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintoolpolicy.DefaultSettings(), nil
	}
	if err != nil {
		return domaintoolpolicy.Settings{}, fmt.Errorf("toolpolicy: get: %w", err)
	}
	return domaintoolpolicy.Settings{
		Mode:            domaintoolpolicy.NormMode(mode),
		Enabled:         decodeJSONList[string](enabled.String),
		Disabled:        decodeJSONList[string](disabled.String),
		BlockedPatterns: decodeJSONList[domaintoolpolicy.BlockedPattern](patterns.String),
		BlockedPaths:    decodeJSONList[domaintoolpolicy.BlockedPath](paths.String),
		UpdatedAt:       updatedAt,
		UpdatedBy:       updatedBy.String,
	}, nil
}

// Save 는 정책을 upsert(UPDATE id=1 → 없으면 INSERT) 한다(드라이버 무관 portable upsert).
func (r *ToolPolicyRepository) Save(ctx context.Context, s domaintoolpolicy.Settings) error {
	enabled := encodeJSONList(s.Enabled)
	disabled := encodeJSONList(s.Disabled)
	patterns := encodeJSONList(s.BlockedPatterns)
	paths := encodeJSONList(s.BlockedPaths)

	res, err := r.db.ExecContext(ctx,
		"UPDATE tool_policy_settings SET mode=?, enabled=?, disabled=?, blocked_patterns=?, blocked_paths=?, updated_at=?, updated_by=? WHERE id=1",
		s.Mode, enabled, disabled, patterns, paths, s.UpdatedAt, nullString(s.UpdatedBy))
	if err != nil {
		return fmt.Errorf("toolpolicy: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO tool_policy_settings (id, mode, enabled, disabled, blocked_patterns, blocked_paths, updated_at, updated_by) VALUES (1,?,?,?,?,?,?,?)",
		s.Mode, enabled, disabled, patterns, paths, s.UpdatedAt, nullString(s.UpdatedBy)); err != nil {
		return fmt.Errorf("toolpolicy: insert: %w", err)
	}
	return nil
}
