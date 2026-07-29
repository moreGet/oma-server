// Package authapp 는 인증/인가(멤버·역할) 유스케이스를 담는다.
// 포트만 조합하며 외부 라이브러리는 import 하지 않는다(스펙 §3.4).
package authapp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainauth.Service = (*AuthUseCase)(nil)

// AuthUseCase 는 로그인·멤버 관리·인가 게이트를 조율하는 유스케이스다.
type AuthUseCase struct {
	members domainauth.Repository
	roles   domainauth.RoleRepository
	hasher  domainauth.PasswordHasher
	tokens  domainauth.TokenService
}

// NewAuthUseCase 는 의존성을 주입받아 AuthUseCase 를 생성한다.
func NewAuthUseCase(
	members domainauth.Repository,
	roles domainauth.RoleRepository,
	hasher domainauth.PasswordHasher,
	tokens domainauth.TokenService,
) *AuthUseCase {
	return &AuthUseCase{members: members, roles: roles, hasher: hasher, tokens: tokens}
}

// now 는 도메인 시각 규칙(UTC, 초 단위 절삭)을 따른다.
func (u *AuthUseCase) now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// --- 인증 ---

// Login 은 username/password 를 검증하고 JWT 와 멤버를 반환한다.
func (u *AuthUseCase) Login(ctx context.Context, cmd domainauth.LoginCommand) (string, domainauth.Member, error) {
	member, err := u.members.FindByUsername(ctx, cmd.Username)
	if err != nil {
		// 존재하지 않는 사용자도 자격증명 실패로 통일(사용자 열거 방지).
		slog.Warn("login failed", "event", "auth.login", "username", cmd.Username, "reason", "unknown_user")
		return "", domainauth.Member{}, domainauth.ErrInvalidCredentials
	}
	if !member.Active {
		slog.Warn("login failed", "event", "auth.login", "username", cmd.Username, "reason", "inactive")
		return "", domainauth.Member{}, domainauth.ErrInvalidCredentials
	}
	if err := u.hasher.Compare(member.PasswordHash, cmd.Password); err != nil {
		slog.Warn("login failed", "event", "auth.login", "username", cmd.Username, "reason", "bad_password")
		return "", domainauth.Member{}, domainauth.ErrInvalidCredentials
	}
	token, err := u.tokens.Generate(member)
	if err != nil {
		return "", domainauth.Member{}, fmt.Errorf("generate token: %w", err)
	}
	// 속성 키로 "level" 을 쓰면 slog 내장 레벨 키와 충돌해 JSON 에 level 이 두 번 실린다.
	// 파서는 대개 뒤엣것을 취하므로 레코드의 심각도가 역할 레벨 숫자로 덮어써진다(= 감사 로그가 INFO 로 안 잡힘).
	slog.Info("login succeeded", "event", "auth.login", "username", member.Username, "member_id", member.ID, "role_level", int(member.Role.Level))
	return token, member, nil
}

// --- 인가 게이트(§4.4) ---

// RequireActiveMember 는 actor 가 존재하고 활성 상태인지 확인하고 멤버를 반환한다.
func (u *AuthUseCase) RequireActiveMember(ctx context.Context, actorID string) (domainauth.Member, error) {
	actor, err := u.members.FindByID(ctx, actorID)
	if err != nil {
		return domainauth.Member{}, err
	}
	if !actor.Active {
		return domainauth.Member{}, domainauth.ErrPermission
	}
	return actor, nil
}

// RequireAdmin 은 actor 가 admin 이상인지 확인한다.
func (u *AuthUseCase) RequireAdmin(ctx context.Context, actorID string) error {
	actor, err := u.RequireActiveMember(ctx, actorID)
	if err != nil {
		return err
	}
	if actor.Role.Level < domainauth.RoleLevelAdmin {
		return domainauth.ErrPermission
	}
	return nil
}

// --- 멤버 관리(§8.1) ---

// ListMembers 는 admin↑ 만 호출 가능한 멤버 목록을 반환한다.
func (u *AuthUseCase) ListMembers(ctx context.Context, actorID string, filter domainauth.MemberFilter) ([]domainauth.Member, int, error) {
	if err := u.RequireAdmin(ctx, actorID); err != nil {
		return nil, 0, err
	}
	return u.members.List(ctx, filter)
}

// CountMembersByRole 은 admin↑ 만 호출 가능한 역할별 인원 집계를 반환한다(역할ID→인원, total).
// 카운트만 필요한 화면이 ListMembers 로 멤버 전량을 적재하지 않게 하는 경로다.
func (u *AuthUseCase) CountMembersByRole(ctx context.Context, actorID string) (map[int]int, int, error) {
	if err := u.RequireAdmin(ctx, actorID); err != nil {
		return nil, 0, err
	}
	return u.members.CountByRole(ctx)
}

// GetMember 는 본인 또는 admin↑ 만 조회 가능하다.
func (u *AuthUseCase) GetMember(ctx context.Context, actorID, targetID string) (domainauth.Member, error) {
	actor, err := u.RequireActiveMember(ctx, actorID)
	if err != nil {
		return domainauth.Member{}, err
	}
	if actor.ID != targetID && actor.Role.Level < domainauth.RoleLevelAdmin {
		return domainauth.Member{}, domainauth.ErrPermission
	}
	if actor.ID == targetID {
		return actor, nil
	}
	return u.members.FindByID(ctx, targetID)
}

// CreateMember 는 새 멤버를 생성한다. actor 는 admin↑ 이며 생성 역할보다 상위여야 한다.
func (u *AuthUseCase) CreateMember(ctx context.Context, cmd domainauth.CreateMemberCommand) (domainauth.Member, error) {
	if err := cmd.Validate(); err != nil {
		return domainauth.Member{}, err
	}
	actor, err := u.RequireActiveMember(ctx, cmd.ActorID)
	if err != nil {
		return domainauth.Member{}, err
	}
	if actor.Role.Level < domainauth.RoleLevelAdmin {
		return domainauth.Member{}, domainauth.ErrPermission
	}
	targetLevel := domainauth.LevelForRoleID(cmd.RoleID)
	if !actor.Role.Level.CanControl(targetLevel) {
		return domainauth.Member{}, domainauth.ErrPermission
	}

	// 중복 username 사전 검사(repo 의 UNIQUE 제약과 이중 방어).
	if _, err := u.members.FindByUsername(ctx, cmd.Username); err == nil {
		return domainauth.Member{}, domainauth.ErrConflict
	}

	hash, err := u.hasher.Hash(cmd.Password)
	if err != nil {
		return domainauth.Member{}, fmt.Errorf("hash password: %w", err)
	}
	now := u.now()
	member := domainauth.Member{
		ID:           uuid.NewString(),
		Username:     cmd.Username,
		PasswordHash: hash,
		Active:       true,
		Role: domainauth.Role{
			ID:    cmd.RoleID,
			Name:  domainauth.NameForRoleID(cmd.RoleID),
			Level: targetLevel,
		},
		Email:        strings.TrimSpace(cmd.Email),
		DisplayName:  strings.TrimSpace(cmd.DisplayName),
		Organization: strings.TrimSpace(cmd.Organization),
		CreatedAt:    now,
		UpdatedAt:    now,
		CreatedBy:    cmd.ActorID,
		UpdatedBy:    cmd.ActorID,
	}
	if err := u.members.Save(ctx, member); err != nil {
		return domainauth.Member{}, err
	}
	slog.Info("member created", "event", "member.created",
		"actor", cmd.ActorID, "member_id", member.ID, "username", member.Username, "role_id", member.Role.ID)
	return member, nil
}

// UpdateProfile 은 멤버 프로필(표시명/소속/이메일)을 변경한다. 본인 또는 CanControl 하위 멤버만 가능.
func (u *AuthUseCase) UpdateProfile(ctx context.Context, cmd domainauth.UpdateProfileCommand) (domainauth.Member, error) {
	if cmd.ActorID != cmd.TargetID {
		if _, _, err := u.requireControl(ctx, cmd.ActorID, cmd.TargetID); err != nil {
			return domainauth.Member{}, err
		}
	} else if _, err := u.RequireActiveMember(ctx, cmd.ActorID); err != nil {
		return domainauth.Member{}, err
	}
	now := u.now()
	if err := u.members.UpdateProfile(ctx, cmd.TargetID,
		strings.TrimSpace(cmd.Email), strings.TrimSpace(cmd.DisplayName), strings.TrimSpace(cmd.Organization),
		now.Unix(), cmd.ActorID); err != nil {
		return domainauth.Member{}, err
	}
	slog.Info("member profile updated", "event", "member.profile_updated",
		"actor", cmd.ActorID, "member_id", cmd.TargetID)
	return u.members.FindByID(ctx, cmd.TargetID)
}

// ChangeRole 은 멤버 역할을 변경한다. actor 는 admin↑ 이며 대상·신규역할 모두 제어 가능해야 한다.
func (u *AuthUseCase) ChangeRole(ctx context.Context, cmd domainauth.ChangeRoleCommand) (domainauth.Member, error) {
	if err := cmd.Validate(); err != nil {
		return domainauth.Member{}, err
	}
	actor, target, err := u.requireControl(ctx, cmd.ActorID, cmd.TargetID)
	if err != nil {
		return domainauth.Member{}, err
	}
	newLevel := domainauth.LevelForRoleID(cmd.RoleID)
	if !actor.Role.Level.CanControl(newLevel) {
		return domainauth.Member{}, domainauth.ErrPermission
	}
	now := u.now()
	target.Role = domainauth.Role{ID: cmd.RoleID, Name: domainauth.NameForRoleID(cmd.RoleID), Level: newLevel}
	target.UpdatedAt = now
	target.UpdatedBy = cmd.ActorID
	if err := u.members.Update(ctx, target); err != nil {
		return domainauth.Member{}, err
	}
	slog.Info("member role changed", "event", "member.role_changed",
		"actor", cmd.ActorID, "target", cmd.TargetID, "role_id", cmd.RoleID)
	return target, nil
}

// SetActive 는 멤버 활성/비활성을 토글한다. actor 는 admin↑ 이며 대상을 제어 가능해야 한다.
func (u *AuthUseCase) SetActive(ctx context.Context, cmd domainauth.SetActiveCommand) (domainauth.Member, error) {
	_, target, err := u.requireControl(ctx, cmd.ActorID, cmd.TargetID)
	if err != nil {
		return domainauth.Member{}, err
	}
	now := u.now()
	target.Active = cmd.Active
	target.UpdatedAt = now
	target.UpdatedBy = cmd.ActorID
	if err := u.members.Update(ctx, target); err != nil {
		return domainauth.Member{}, err
	}
	slog.Info("member active changed", "event", "member.active_changed",
		"actor", cmd.ActorID, "target", cmd.TargetID, "active", cmd.Active)
	return target, nil
}

// DeleteMember 는 멤버를 삭제한다. actor 는 대상을 제어 가능해야 한다(라우트에서 super_admin 게이트).
func (u *AuthUseCase) DeleteMember(ctx context.Context, actorID, targetID string) error {
	if _, _, err := u.requireControl(ctx, actorID, targetID); err != nil {
		return err
	}
	if err := u.members.Delete(ctx, targetID); err != nil {
		return err
	}
	slog.Info("member deleted", "event", "member.deleted", "actor", actorID, "target", targetID)
	return nil
}

// --- 비밀번호 / 역할 ---

// ChangePassword 는 본인 비밀번호를 변경한다(기존 비번 확인 필요).
func (u *AuthUseCase) ChangePassword(ctx context.Context, actorID, oldPassword, newPassword string) error {
	actor, err := u.RequireActiveMember(ctx, actorID)
	if err != nil {
		return err
	}
	if err := u.hasher.Compare(actor.PasswordHash, oldPassword); err != nil {
		return domainauth.ErrInvalidCredentials
	}
	if len(newPassword) < domainauth.MinPasswordLength {
		return &domainauth.ErrValidation{Msg: "password must be >= 8 chars"}
	}
	hash, err := u.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := u.members.UpdatePassword(ctx, actor.ID, hash, u.now().Unix(), actor.ID); err != nil {
		return err
	}
	slog.Info("password changed", "event", "member.password_changed", "actor", actor.ID)
	return nil
}

// ResetPassword 는 admin↑ 가 제어 가능한 하위 멤버의 비밀번호를 리셋한다(기존 비번 불필요).
func (u *AuthUseCase) ResetPassword(ctx context.Context, actorID, targetID, newPassword string) error {
	actor, _, err := u.requireControl(ctx, actorID, targetID)
	if err != nil {
		return err
	}
	if len(newPassword) < domainauth.MinPasswordLength {
		return &domainauth.ErrValidation{Msg: "password must be >= 8 chars"}
	}
	hash, err := u.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := u.members.UpdatePassword(ctx, targetID, hash, u.now().Unix(), actor.ID); err != nil {
		return err
	}
	slog.Info("member password reset", "event", "member.password_reset", "actor", actor.ID, "target", targetID)
	return nil
}

// ListRoles 는 역할 목록(마스터데이터)을 반환한다.
func (u *AuthUseCase) ListRoles(ctx context.Context) ([]domainauth.Role, error) {
	return u.roles.List(ctx)
}

// requireControl 은 actor 가 admin↑ 이고 target 을 제어 가능(상위 레벨)한지 검증하고 둘을 반환한다.
func (u *AuthUseCase) requireControl(ctx context.Context, actorID, targetID string) (domainauth.Member, domainauth.Member, error) {
	actor, err := u.RequireActiveMember(ctx, actorID)
	if err != nil {
		return domainauth.Member{}, domainauth.Member{}, err
	}
	if actor.Role.Level < domainauth.RoleLevelAdmin {
		slog.Warn("member control denied", "event", "member.denied", "actor", actorID, "target", targetID, "reason", "not_admin")
		return domainauth.Member{}, domainauth.Member{}, domainauth.ErrPermission
	}
	target, err := u.members.FindByID(ctx, targetID)
	if err != nil {
		return domainauth.Member{}, domainauth.Member{}, err
	}
	if !actor.Role.Level.CanControl(target.Role.Level) {
		slog.Warn("member control denied", "event", "member.denied", "actor", actorID, "target", targetID, "reason", "cannot_control")
		return domainauth.Member{}, domainauth.Member{}, domainauth.ErrPermission
	}
	return actor, target, nil
}
