// Package auth 는 인증/인가(멤버·역할) 어그리거트의 도메인 모델·포트·순수 로직을 담는다.
// 도메인 규칙(스펙 §1): 외부 import 금지. stdlib(errors/strings/time)만 사용한다.
package auth

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러(핸들러가 HTTP 코드로 매핑) ---
var (
	ErrNotFound           = errors.New("member not found")      // → 404
	ErrPermission         = errors.New("permission denied")     // → 403
	ErrInvalidCredentials = errors.New("invalid credentials")   // → 401
	ErrInvalidToken       = errors.New("invalid token")         // → 401
	ErrConflict           = errors.New("member already exists") // → 409
)

// ErrValidation 은 입력 검증 실패를 나타내는 typed 에러다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// --- 역할 계층(§4.1) ---

// RoleLevel 은 권한 비교용 코드 상수다(role_id 와 구분).
type RoleLevel int

const (
	RoleLevelUser       RoleLevel = 0
	RoleLevelAdmin      RoleLevel = 1
	RoleLevelSuperAdmin RoleLevel = 2
)

// CanControl 은 actor 가 target 보다 상위일 때만 true(상위만 하위를 제어).
func (actor RoleLevel) CanControl(target RoleLevel) bool { return actor > target }

// role_id 값(DB·API 노출). 권한 비교용 RoleLevel(0/1/2)과 구분한다(§4.1).
const (
	RoleIDUser       = 1
	RoleIDAdmin      = 2
	RoleIDSuperAdmin = 3
)

// MinPasswordLength 는 멤버 비밀번호 최소 길이다.
const MinPasswordLength = 8

// IsValidRoleID 는 role_id 가 알려진 범위(user/admin/super_admin)인지 판정한다.
func IsValidRoleID(roleID int) bool {
	return roleID >= RoleIDUser && roleID <= RoleIDSuperAdmin
}

// Role 은 role_id(1/2/3, DB·API 노출) ↔ role_level(0/1/2, 코드 비교)을 함께 담는 마스터데이터다.
type Role struct {
	ID    int       // 1=user, 2=admin, 3=super_admin
	Name  string    // "user" / "admin" / "super_admin"
	Level RoleLevel // 0/1/2
}

// --- 엔티티 ---

// Member 는 인증 주체다.
type Member struct {
	ID           string // UUID v4
	Username     string
	PasswordHash string // bcrypt; 응답 DTO 노출 금지
	Active       bool
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CreatedBy    string // "" = 시스템
	UpdatedBy    string
}

// --- JWT 클레임(§4.6) ---

// Claims 는 토큰에 담기는 인증 주체 정보다.
type Claims struct {
	MemberID string
	Username string
	Level    RoleLevel
}

// --- 커맨드 ---

// LoginCommand 는 로그인 입력이다.
type LoginCommand struct {
	Username string
	Password string
}

// CreateMemberCommand 는 멤버 생성 입력이다.
type CreateMemberCommand struct {
	Username string
	Password string
	RoleID   int
	ActorID  string
}

// ChangeRoleCommand 는 멤버 역할 변경 입력이다.
type ChangeRoleCommand struct {
	ActorID  string
	TargetID string
	RoleID   int
}

// SetActiveCommand 는 멤버 활성/비활성 토글 입력이다.
type SetActiveCommand struct {
	ActorID  string
	TargetID string
	Active   bool
}

// MemberFilter 는 목록 조회 필터다. RoleID 0 = 전체.
type MemberFilter struct {
	RoleID int
	Limit  int
	Offset int
}

// Normalize 는 입력을 정규화한다.
func (c *CreateMemberCommand) Normalize() { c.Username = strings.TrimSpace(c.Username) }

// Validate 는 멤버 생성 입력을 검증한다.
func (c *CreateMemberCommand) Validate() error {
	c.Normalize()
	if c.Username == "" {
		return &ErrValidation{Msg: "username is required"}
	}
	if len(c.Password) < MinPasswordLength {
		return &ErrValidation{Msg: "password must be >= 8 chars"}
	}
	if !IsValidRoleID(c.RoleID) {
		return &ErrValidation{Msg: "invalid role_id"}
	}
	return nil
}

// Validate 는 역할 변경 입력을 검증한다.
func (c *ChangeRoleCommand) Validate() error {
	if !IsValidRoleID(c.RoleID) {
		return &ErrValidation{Msg: "invalid role_id"}
	}
	return nil
}
