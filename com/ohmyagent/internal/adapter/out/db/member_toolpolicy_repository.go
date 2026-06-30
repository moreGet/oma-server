package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintoolpolicy.MemberPolicyRepository = (*MemberToolPolicyRepository)(nil)

// MemberToolPolicyRepository 는 멤버별 도구 정책 오버라이드를 member_tool_policy 테이블에 보관한다.
// 가변 길이 목록(enabled/disabled)은 JSON TEXT 컬럼으로 저장한다(전역 레포와 동일 인코딩 재사용).
type MemberToolPolicyRepository struct {
	db *sql.DB
}

// NewMemberToolPolicyRepository 는 MemberToolPolicyRepository 를 생성한다.
func NewMemberToolPolicyRepository(conn *sql.DB) *MemberToolPolicyRepository {
	return &MemberToolPolicyRepository{db: conn}
}

const memberToolPolicyCols = "member_id, enabled, disabled, updated_at, updated_by"

// Get 은 멤버 정책을 반환한다(행이 없으면 빈 MemberPolicy{MemberID}).
func (r *MemberToolPolicyRepository) Get(ctx context.Context, memberID string) (domaintoolpolicy.MemberPolicy, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+memberToolPolicyCols+" FROM member_tool_policy WHERE member_id=?", memberID)
	p, err := scanMemberToolPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintoolpolicy.MemberPolicy{MemberID: memberID}, nil
	}
	if err != nil {
		return domaintoolpolicy.MemberPolicy{}, fmt.Errorf("member toolpolicy: get: %w", err)
	}
	return p, nil
}

// All 은 모든 멤버 정책을 반환한다(매니저 캐시 적재용).
func (r *MemberToolPolicyRepository) All(ctx context.Context) ([]domaintoolpolicy.MemberPolicy, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+memberToolPolicyCols+" FROM member_tool_policy")
	if err != nil {
		return nil, fmt.Errorf("member toolpolicy: all: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domaintoolpolicy.MemberPolicy
	for rows.Next() {
		p, err := scanMemberToolPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("member toolpolicy: scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("member toolpolicy: rows: %w", err)
	}
	return out, nil
}

// Save 는 멤버 정책을 upsert(UPDATE → 없으면 INSERT) 한다(드라이버 무관 portable upsert).
func (r *MemberToolPolicyRepository) Save(ctx context.Context, p domaintoolpolicy.MemberPolicy) error {
	enabled := encodeList(p.Enabled)
	disabled := encodeList(p.Disabled)

	res, err := r.db.ExecContext(ctx,
		"UPDATE member_tool_policy SET enabled=?, disabled=?, updated_at=?, updated_by=? WHERE member_id=?",
		enabled, disabled, p.UpdatedAt, nullString(p.UpdatedBy), p.MemberID)
	if err != nil {
		return fmt.Errorf("member toolpolicy: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO member_tool_policy ("+memberToolPolicyCols+") VALUES (?,?,?,?,?)",
		p.MemberID, enabled, disabled, p.UpdatedAt, nullString(p.UpdatedBy)); err != nil {
		return fmt.Errorf("member toolpolicy: insert: %w", err)
	}
	return nil
}

// Delete 는 멤버 정책 행을 제거한다(오버라이드 해제). 없는 행 삭제는 no-op.
func (r *MemberToolPolicyRepository) Delete(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM member_tool_policy WHERE member_id=?", memberID); err != nil {
		return fmt.Errorf("member toolpolicy: delete: %w", err)
	}
	return nil
}

// scanMemberToolPolicy 는 단일 행을 MemberPolicy 로 스캔한다(*sql.Row/*sql.Rows 공용).
func scanMemberToolPolicy(s interface{ Scan(...any) error }) (domaintoolpolicy.MemberPolicy, error) {
	var (
		memberID          string
		enabled, disabled sql.NullString
		updatedAt         int64
		updatedBy         sql.NullString
	)
	if err := s.Scan(&memberID, &enabled, &disabled, &updatedAt, &updatedBy); err != nil {
		return domaintoolpolicy.MemberPolicy{}, err
	}
	return domaintoolpolicy.MemberPolicy{
		MemberID:  memberID,
		Enabled:   decodeStringList(enabled.String),
		Disabled:  decodeStringList(disabled.String),
		UpdatedAt: updatedAt,
		UpdatedBy: updatedBy.String,
	}, nil
}
