package db

import (
	"context"
	"database/sql"
	"fmt"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainauth.RoleRepository = (*RoleRepository)(nil)

// RoleRepository 는 마스터데이터 roles 를 조회한다(domainauth.RoleRepository 구현).
type RoleRepository struct {
	db *sql.DB
}

// NewRoleRepository 는 RoleRepository 를 생성한다.
func NewRoleRepository(conn *sql.DB) *RoleRepository {
	return &RoleRepository{db: conn}
}

const roleColumns = "id, name, level"

// FindByID 는 단건 조회. 없으면 domainauth.ErrNotFound.
func (r *RoleRepository) FindByID(ctx context.Context, id int) (domainauth.Role, error) {
	return queryOne(ctx, r.db, fmt.Sprintf("find role id=%d", id), domainauth.ErrNotFound, scanRole,
		"SELECT "+roleColumns+" FROM roles WHERE id=?", id)
}

// List 는 전체 role 목록을 반환한다.
func (r *RoleRepository) List(ctx context.Context) ([]domainauth.Role, error) {
	return queryList(ctx, r.db, "list roles", scanRole, "SELECT "+roleColumns+" FROM roles ORDER BY id")
}

// scanRole 은 한 행을 domainauth.Role 로 스캔한다.
func scanRole(s rowScanner) (domainauth.Role, error) {
	var (
		role  domainauth.Role
		level int
	)
	if err := s.Scan(&role.ID, &role.Name, &level); err != nil {
		return domainauth.Role{}, err
	}
	role.Level = domainauth.RoleLevel(level)
	return role, nil
}
