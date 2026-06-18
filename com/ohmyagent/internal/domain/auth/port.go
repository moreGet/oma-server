package auth

import "context"

// Service — in 포트(유스케이스가 노출하는 능력). main.go 가 핸들러에 주입한다.
type Service interface {
	// 인증
	Login(ctx context.Context, cmd LoginCommand) (string, Member, error) // 토큰, 멤버, err

	// 멤버 관리(§8.1)
	ListMembers(ctx context.Context, actorID string, filter MemberFilter) ([]Member, int, error)
	GetMember(ctx context.Context, actorID, targetID string) (Member, error)
	CreateMember(ctx context.Context, cmd CreateMemberCommand) (Member, error)
	ChangeRole(ctx context.Context, cmd ChangeRoleCommand) (Member, error)
	SetActive(ctx context.Context, cmd SetActiveCommand) (Member, error)
	DeleteMember(ctx context.Context, actorID, targetID string) error

	// 인가 게이트(타 도메인이 accessGate 로 재사용; §4.4)
	RequireActiveMember(ctx context.Context, actorID string) (Member, error)
	RequireAdmin(ctx context.Context, actorID string) error
	EnsureProjectAccess(ctx context.Context, actorID string, projectID int) error
}

// Repository — out 포트(멤버 영속화).
type Repository interface {
	Save(ctx context.Context, m Member) error                             // INSERT
	Update(ctx context.Context, m Member) error                           // UPDATE(role/active/audit)
	FindByID(ctx context.Context, id string) (Member, error)              // 없으면 ErrNotFound
	FindByUsername(ctx context.Context, username string) (Member, error)  // 없으면 ErrNotFound
	List(ctx context.Context, filter MemberFilter) ([]Member, int, error) // total 포함
	Delete(ctx context.Context, id string) error                          // 0행 → ErrNotFound
}

// RoleRepository — out 포트(마스터데이터 roles).
type RoleRepository interface {
	FindByID(ctx context.Context, id int) (Role, error) // 없으면 ErrNotFound
	List(ctx context.Context) ([]Role, error)
}

// TokenService — out 포트(JWT 발급/검증). security.JWTTokenService 가 구현.
type TokenService interface {
	Generate(member Member) (string, error)
	Parse(token string) (Claims, error) // 실패 시 ErrInvalidToken
}

// PasswordHasher — out 포트(bcrypt). authout.BcryptHasher 가 구현.
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) error // 불일치 시 ErrInvalidCredentials
}
