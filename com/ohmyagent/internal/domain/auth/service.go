package auth

// service.go — 부수효과 없는 순수 도메인 판정/매핑 로직(스펙 §3.3).
// CanControl 은 model.go(RoleLevel)에 둔다. 여기서는 role_id ↔ RoleLevel 매핑만 제공한다.

// LevelForRoleID 는 role_id(1/2/3) 를 RoleLevel(0/1/2) 로 매핑한다.
// 알 수 없는 id 는 RoleLevelUser 로 폴백한다(권한 최소화 원칙).
func LevelForRoleID(roleID int) RoleLevel {
	switch roleID {
	case RoleIDUser:
		return RoleLevelUser
	case RoleIDAdmin:
		return RoleLevelAdmin
	case RoleIDSuperAdmin:
		return RoleLevelSuperAdmin
	default:
		return RoleLevelUser
	}
}

// NameForRoleID 는 role_id 를 표준 역할명으로 매핑한다.
func NameForRoleID(roleID int) string {
	switch roleID {
	case RoleIDUser:
		return "user"
	case RoleIDAdmin:
		return "admin"
	case RoleIDSuperAdmin:
		return "super_admin"
	default:
		return "user"
	}
}
