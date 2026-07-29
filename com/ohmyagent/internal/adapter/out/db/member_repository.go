package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainauth.Repository = (*MemberRepository)(nil)

// MemberRepository 는 members 영속화를 담당한다(domainauth.Repository 구현).
// 조회 시 roles 를 JOIN 하여 Member.Role(id/name/level) 을 채운다.
type MemberRepository struct {
	db *sql.DB
}

// NewMemberRepository 는 MemberRepository 를 생성한다.
func NewMemberRepository(conn *sql.DB) *MemberRepository {
	return &MemberRepository{db: conn}
}

// 조회용 SELECT: members + roles JOIN. 컬럼 순서는 scanMember 와 일치해야 한다.
const memberSelect = `SELECT
    m.id, m.username, m.password_hash, m.active,
    r.id, r.name, r.level,
    m.created_at, m.updated_at, m.created_by, m.updated_by,
    m.email, m.display_name, m.organization
FROM members m
JOIN roles r ON r.id = m.role_id`

// Save 는 새 멤버를 INSERT 한다.
func (r *MemberRepository) Save(ctx context.Context, m domainauth.Member) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO members (id, username, password_hash, active, role_id, created_at, updated_at, created_by, updated_by, email, display_name, organization) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
		m.ID, m.Username, m.PasswordHash, m.Active, m.Role.ID,
		m.CreatedAt.Unix(), m.UpdatedAt.Unix(), nullString(m.CreatedBy), nullString(m.UpdatedBy),
		nullString(m.Email), nullString(m.DisplayName), nullString(m.Organization),
	)
	if err != nil {
		return fmt.Errorf("save member: %w", err)
	}
	return nil
}

// UpdateProfile 은 프로필(email/display_name/organization) + audit 를 갱신한다. 0행 → domainauth.ErrNotFound.
func (r *MemberRepository) UpdateProfile(ctx context.Context, id, email, displayName, organization string, updatedAt int64, updatedBy string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE members SET email=?, display_name=?, organization=?, updated_at=?, updated_by=? WHERE id=?",
		nullString(email), nullString(displayName), nullString(organization), updatedAt, nullString(updatedBy), id,
	)
	if err != nil {
		return fmt.Errorf("update profile id=%s: %w", id, err)
	}
	return affectedOrNotFound(res, domainauth.ErrNotFound)
}

// Update 는 role/active/audit 필드를 갱신한다. 0행 → domainauth.ErrNotFound.
func (r *MemberRepository) Update(ctx context.Context, m domainauth.Member) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE members SET role_id=?, active=?, updated_at=?, updated_by=? WHERE id=?",
		m.Role.ID, m.Active, m.UpdatedAt.Unix(), nullString(m.UpdatedBy), m.ID,
	)
	if err != nil {
		return fmt.Errorf("update member id=%s: %w", m.ID, err)
	}
	return affectedOrNotFound(res, domainauth.ErrNotFound)
}

// FindByID 는 단건 조회. 없으면 domainauth.ErrNotFound.
func (r *MemberRepository) FindByID(ctx context.Context, id string) (domainauth.Member, error) {
	row := r.db.QueryRowContext(ctx, memberSelect+" WHERE m.id=?", id)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainauth.Member{}, domainauth.ErrNotFound
	}
	if err != nil {
		return domainauth.Member{}, fmt.Errorf("find member id=%s: %w", id, err)
	}
	return m, nil
}

// FindByUsername 은 username 으로 조회. 없으면 domainauth.ErrNotFound.
func (r *MemberRepository) FindByUsername(ctx context.Context, username string) (domainauth.Member, error) {
	row := r.db.QueryRowContext(ctx, memberSelect+" WHERE m.username=?", username)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domainauth.Member{}, domainauth.ErrNotFound
	}
	if err != nil {
		return domainauth.Member{}, fmt.Errorf("find member username=%s: %w", username, err)
	}
	return m, nil
}

// List 는 필터(role_id, limit/offset)에 맞는 목록과 total 을 반환한다. RoleID 0 = 전체.
func (r *MemberRepository) List(ctx context.Context, filter domainauth.MemberFilter) ([]domainauth.Member, int, error) {
	var (
		where string
		args  []any
	)
	if filter.RoleID > 0 {
		where = " WHERE m.role_id=?"
		args = append(args, filter.RoleID)
	}

	// total 집계
	var total int
	countQuery := "SELECT COUNT(*) FROM members m" + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}

	query := memberSelect + where + " ORDER BY m.created_at DESC"
	listArgs := append([]any{}, args...)
	if filter.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		listArgs = append(listArgs, filter.Limit, filter.Offset)
	}

	rows, err := r.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// total 을 이미 알고 있으므로 정확한 용량으로 한 번에 잡는다(append 재할당·복사 제거).
	// LIMIT 이 걸린 경우엔 그쪽이 상한이다.
	capacity := total
	if filter.Limit > 0 && filter.Limit < capacity {
		capacity = filter.Limit
	}
	out := make([]domainauth.Member, 0, capacity)
	for rows.Next() {
		m, scanErr := scanMember(rows)
		if scanErr != nil {
			return nil, 0, fmt.Errorf("scan member: %w", scanErr)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate members: %w", err)
	}
	return out, total, nil
}

// CountByRole 은 역할별 인원과 전체 인원을 집계한다(멤버 행을 힙에 올리지 않는다).
//
// 대시보드가 역할별 카운트만 필요할 때 List 로 전량을 적재하지 않게 하려는 메서드다 —
// GROUP BY 한 번이면 결과 크기가 멤버 수가 아니라 **역할 수**에 묶인다.
func (r *MemberRepository) CountByRole(ctx context.Context) (map[int]int, int, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT role_id, COUNT(*) FROM members GROUP BY role_id")
	if err != nil {
		return nil, 0, fmt.Errorf("count members by role: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int]int)
	total := 0
	for rows.Next() {
		var roleID, n int
		if err := rows.Scan(&roleID, &n); err != nil {
			return nil, 0, fmt.Errorf("scan member role count: %w", err)
		}
		out[roleID] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate member role counts: %w", err)
	}
	return out, total, nil
}

// UpdatePassword 는 password_hash + 감사필드를 갱신한다. 0행 → domainauth.ErrNotFound.
func (r *MemberRepository) UpdatePassword(ctx context.Context, id, passwordHash string, updatedAt int64, updatedBy string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE members SET password_hash=?, updated_at=?, updated_by=? WHERE id=?",
		passwordHash, updatedAt, nullString(updatedBy), id,
	)
	if err != nil {
		return fmt.Errorf("update member password id=%s: %w", id, err)
	}
	return affectedOrNotFound(res, domainauth.ErrNotFound)
}

// Delete 는 ID 기준 삭제. 0행 → domainauth.ErrNotFound.
func (r *MemberRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM members WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete member id=%s: %w", id, err)
	}
	return affectedOrNotFound(res, domainauth.ErrNotFound)
}

// scanMember 는 한 행(members JOIN roles)을 domainauth.Member 로 스캔한다.
// memberSelect 의 컬럼 순서와 정확히 일치해야 한다.
func scanMember(s rowScanner) (domainauth.Member, error) {
	var (
		m            domainauth.Member
		level        int
		createdAt    int64
		updatedAt    int64
		createdBy    sql.NullString
		updatedBy    sql.NullString
		email        sql.NullString
		displayName  sql.NullString
		organization sql.NullString
	)
	if err := s.Scan(
		&m.ID, &m.Username, &m.PasswordHash, &m.Active,
		&m.Role.ID, &m.Role.Name, &level,
		&createdAt, &updatedAt, &createdBy, &updatedBy,
		&email, &displayName, &organization,
	); err != nil {
		return domainauth.Member{}, err
	}
	m.Role.Level = domainauth.RoleLevel(level)
	m.CreatedAt = time.Unix(createdAt, 0).UTC()
	m.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	m.CreatedBy = strFromNull(createdBy)
	m.UpdatedBy = strFromNull(updatedBy)
	m.Email = strFromNull(email)
	m.DisplayName = strFromNull(displayName)
	m.Organization = strFromNull(organization)
	return m, nil
}
