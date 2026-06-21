package authapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// ---------------------------------------------------------------------------
// Fakes (in-memory, no external mock lib)
// ---------------------------------------------------------------------------

// fakeMemberRepo implements domainauth.Repository.
type fakeMemberRepo struct {
	byID       map[string]domainauth.Member
	byUsername map[string]domainauth.Member
	saved      []domainauth.Member
	updated    []domainauth.Member
	deleted    []string
	listResult []domainauth.Member
	listTotal  int
	listErr    error
	saveErr    error
}

var _ domainauth.Repository = (*fakeMemberRepo)(nil)

func newFakeMemberRepo() *fakeMemberRepo {
	return &fakeMemberRepo{
		byID:       map[string]domainauth.Member{},
		byUsername: map[string]domainauth.Member{},
	}
}

func (r *fakeMemberRepo) add(m domainauth.Member) {
	r.byID[m.ID] = m
	r.byUsername[m.Username] = m
}

func (r *fakeMemberRepo) Save(ctx context.Context, m domainauth.Member) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, m)
	r.add(m)
	return nil
}

func (r *fakeMemberRepo) Update(ctx context.Context, m domainauth.Member) error {
	r.updated = append(r.updated, m)
	r.add(m)
	return nil
}

func (r *fakeMemberRepo) FindByID(ctx context.Context, id string) (domainauth.Member, error) {
	m, ok := r.byID[id]
	if !ok {
		return domainauth.Member{}, domainauth.ErrNotFound
	}
	return m, nil
}

func (r *fakeMemberRepo) FindByUsername(ctx context.Context, username string) (domainauth.Member, error) {
	m, ok := r.byUsername[username]
	if !ok {
		return domainauth.Member{}, domainauth.ErrNotFound
	}
	return m, nil
}

func (r *fakeMemberRepo) List(ctx context.Context, filter domainauth.MemberFilter) ([]domainauth.Member, int, error) {
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	return r.listResult, r.listTotal, nil
}

func (r *fakeMemberRepo) Delete(ctx context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *fakeMemberRepo) UpdatePassword(ctx context.Context, id, passwordHash string, updatedAt int64, updatedBy string) error {
	if m, ok := r.byID[id]; ok {
		m.PasswordHash = passwordHash
		r.add(m)
		return nil
	}
	return domainauth.ErrNotFound
}

// fakeRoleRepo implements domainauth.RoleRepository.
type fakeRoleRepo struct{}

var _ domainauth.RoleRepository = (*fakeRoleRepo)(nil)

func (r *fakeRoleRepo) FindByID(ctx context.Context, id int) (domainauth.Role, error) {
	return domainauth.Role{ID: id, Name: domainauth.NameForRoleID(id), Level: domainauth.LevelForRoleID(id)}, nil
}

func (r *fakeRoleRepo) List(ctx context.Context) ([]domainauth.Role, error) {
	return nil, nil
}

// fakeTokenService implements domainauth.TokenService.
type fakeTokenService struct {
	token   string
	genErr  error
	lastGen domainauth.Member
}

var _ domainauth.TokenService = (*fakeTokenService)(nil)

func (s *fakeTokenService) Generate(member domainauth.Member) (string, error) {
	s.lastGen = member
	if s.genErr != nil {
		return "", s.genErr
	}
	return s.token, nil
}

func (s *fakeTokenService) Parse(token string) (domainauth.Claims, error) {
	return domainauth.Claims{}, domainauth.ErrInvalidToken
}

// fakeHasher implements domainauth.PasswordHasher.
// match=true → Compare returns nil; otherwise ErrInvalidCredentials.
type fakeHasher struct {
	match   bool
	hashOut string
	hashErr error
}

var _ domainauth.PasswordHasher = (*fakeHasher)(nil)

func (h *fakeHasher) Hash(plain string) (string, error) {
	if h.hashErr != nil {
		return "", h.hashErr
	}
	if h.hashOut != "" {
		return h.hashOut, nil
	}
	return "hashed:" + plain, nil
}

func (h *fakeHasher) Compare(hash, plain string) error {
	if h.match {
		return nil
	}
	return domainauth.ErrInvalidCredentials
}

// member is a helper to build a member with a level-derived role.
func member(id, username string, roleID int, active bool) domainauth.Member {
	return domainauth.Member{
		ID:           id,
		Username:     username,
		PasswordHash: "hashed",
		Active:       active,
		Role: domainauth.Role{
			ID:    roleID,
			Name:  domainauth.NameForRoleID(roleID),
			Level: domainauth.LevelForRoleID(roleID),
		},
	}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestAuthUseCase_Login(t *testing.T) {
	ctx := context.Background()

	t.Run("success returns token and member", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 2, true))
		hasher := &fakeHasher{match: true}
		tokens := &fakeTokenService{token: "tok-123"}
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, hasher, tokens)

		token, m, err := uc.Login(ctx, domainauth.LoginCommand{Username: "alice", Password: "pw"})
		require.NoError(t, err)
		assert.Equal(t, "tok-123", token)
		assert.Equal(t, "u1", m.ID)
		assert.Equal(t, "u1", tokens.lastGen.ID)
	})

	t.Run("password mismatch returns ErrInvalidCredentials", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 2, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: false}, &fakeTokenService{token: "x"})

		_, _, err := uc.Login(ctx, domainauth.LoginCommand{Username: "alice", Password: "wrong"})
		assert.ErrorIs(t, err, domainauth.ErrInvalidCredentials)
	})

	t.Run("unknown user returns ErrInvalidCredentials", func(t *testing.T) {
		repo := newFakeMemberRepo()
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{token: "x"})

		_, _, err := uc.Login(ctx, domainauth.LoginCommand{Username: "ghost", Password: "pw"})
		assert.ErrorIs(t, err, domainauth.ErrInvalidCredentials)
	})

	t.Run("inactive user returns ErrInvalidCredentials", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 2, false))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{token: "x"})

		_, _, err := uc.Login(ctx, domainauth.LoginCommand{Username: "alice", Password: "pw"})
		assert.ErrorIs(t, err, domainauth.ErrInvalidCredentials)
	})
}

// ---------------------------------------------------------------------------
// CreateMember
// ---------------------------------------------------------------------------

func TestAuthUseCase_CreateMember(t *testing.T) {
	ctx := context.Background()

	t.Run("success fills uuid, timestamps and audit fields", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 3, true)) // super_admin actor
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true, hashOut: "H"}, &fakeTokenService{})

		m, err := uc.CreateMember(ctx, domainauth.CreateMemberCommand{
			Username: "newuser",
			Password: "password1",
			RoleID:   1,
			ActorID:  "admin1",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, m.ID)
		assert.Equal(t, "newuser", m.Username)
		assert.Equal(t, "H", m.PasswordHash)
		assert.True(t, m.Active)
		assert.Equal(t, 1, m.Role.ID)
		assert.Equal(t, domainauth.RoleLevelUser, m.Role.Level)
		assert.False(t, m.CreatedAt.IsZero())
		assert.False(t, m.UpdatedAt.IsZero())
		assert.Equal(t, "admin1", m.CreatedBy)
		assert.Equal(t, "admin1", m.UpdatedBy)
		require.Len(t, repo.saved, 1)
	})

	t.Run("duplicate username returns ErrConflict", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 3, true))
		repo.add(member("dup", "taken", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{})

		_, err := uc.CreateMember(ctx, domainauth.CreateMemberCommand{
			Username: "taken",
			Password: "password1",
			RoleID:   1,
			ActorID:  "admin1",
		})
		assert.ErrorIs(t, err, domainauth.ErrConflict)
	})

	t.Run("validation error on bad command", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 3, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{})

		_, err := uc.CreateMember(ctx, domainauth.CreateMemberCommand{
			Username: "x",
			Password: "short",
			RoleID:   1,
			ActorID:  "admin1",
		})
		var ve *domainauth.ErrValidation
		assert.True(t, errors.As(err, &ve))
	})

	t.Run("non-admin actor is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("user1", "user", 1, true)) // user level
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{})

		_, err := uc.CreateMember(ctx, domainauth.CreateMemberCommand{
			Username: "newuser",
			Password: "password1",
			RoleID:   1,
			ActorID:  "user1",
		})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("admin cannot create equal-or-higher role (CanControl)", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 2, true)) // admin level
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{match: true}, &fakeTokenService{})

		// creating another admin (role 2) → CanControl(admin,admin)=false → ErrPermission
		_, err := uc.CreateMember(ctx, domainauth.CreateMemberCommand{
			Username: "newadmin",
			Password: "password1",
			RoleID:   2,
			ActorID:  "admin1",
		})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})
}

// ---------------------------------------------------------------------------
// ChangeRole / SetActive / Delete — CanControl authorization
// ---------------------------------------------------------------------------

func TestAuthUseCase_ChangeRole(t *testing.T) {
	ctx := context.Background()

	t.Run("super_admin can change a user's role", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("super", "super", 3, true))
		repo.add(member("target", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		m, err := uc.ChangeRole(ctx, domainauth.ChangeRoleCommand{ActorID: "super", TargetID: "target", RoleID: 2})
		require.NoError(t, err)
		assert.Equal(t, 2, m.Role.ID)
		require.Len(t, repo.updated, 1)
		assert.Equal(t, "super", m.UpdatedBy)
	})

	t.Run("invalid role_id returns validation error", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("super", "super", 3, true))
		repo.add(member("target", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		_, err := uc.ChangeRole(ctx, domainauth.ChangeRoleCommand{ActorID: "super", TargetID: "target", RoleID: 9})
		var ve *domainauth.ErrValidation
		assert.True(t, errors.As(err, &ve))
	})

	t.Run("actor not higher than target is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin", "admin", 2, true))
		repo.add(member("target", "other", 2, true)) // equal level
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		_, err := uc.ChangeRole(ctx, domainauth.ChangeRoleCommand{ActorID: "admin", TargetID: "target", RoleID: 1})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("actor cannot assign role it does not control", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin", "admin", 2, true))
		repo.add(member("target", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		// admin tries to promote target to super_admin (role 3) → CanControl(admin,super)=false
		_, err := uc.ChangeRole(ctx, domainauth.ChangeRoleCommand{ActorID: "admin", TargetID: "target", RoleID: 3})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})
}

func TestAuthUseCase_SetActive(t *testing.T) {
	ctx := context.Background()

	t.Run("super_admin can deactivate a user", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("super", "super", 3, true))
		repo.add(member("target", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		m, err := uc.SetActive(ctx, domainauth.SetActiveCommand{ActorID: "super", TargetID: "target", Active: false})
		require.NoError(t, err)
		assert.False(t, m.Active)
		require.Len(t, repo.updated, 1)
	})

	t.Run("equal level actor is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin", "admin", 2, true))
		repo.add(member("target", "other", 2, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		_, err := uc.SetActive(ctx, domainauth.SetActiveCommand{ActorID: "admin", TargetID: "target", Active: false})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})
}

func TestAuthUseCase_DeleteMember(t *testing.T) {
	ctx := context.Background()

	t.Run("super_admin can delete a lower member", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("super", "super", 3, true))
		repo.add(member("target", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		err := uc.DeleteMember(ctx, "super", "target")
		require.NoError(t, err)
		assert.Equal(t, []string{"target"}, repo.deleted)
	})

	t.Run("lower-or-equal actor is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin", "admin", 2, true))
		repo.add(member("target", "other", 2, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		err := uc.DeleteMember(ctx, "admin", "target")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
		assert.Empty(t, repo.deleted)
	})
}

// ---------------------------------------------------------------------------
// GetMember
// ---------------------------------------------------------------------------

func TestAuthUseCase_GetMember(t *testing.T) {
	ctx := context.Background()

	t.Run("self can read own profile", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		m, err := uc.GetMember(ctx, "u1", "u1")
		require.NoError(t, err)
		assert.Equal(t, "u1", m.ID)
	})

	t.Run("admin can read another member", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 2, true))
		repo.add(member("u1", "alice", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		m, err := uc.GetMember(ctx, "admin1", "u1")
		require.NoError(t, err)
		assert.Equal(t, "u1", m.ID)
	})

	t.Run("non-admin reading another member is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 1, true))
		repo.add(member("u2", "bob", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})

		_, err := uc.GetMember(ctx, "u1", "u2")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})
}

// ---------------------------------------------------------------------------
// RequireAdmin
// ---------------------------------------------------------------------------

func TestAuthUseCase_RequireAdmin(t *testing.T) {
	ctx := context.Background()

	t.Run("admin passes", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 2, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})
		assert.NoError(t, uc.RequireAdmin(ctx, "admin1"))
	})

	t.Run("user is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("u1", "alice", 1, true))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})
		assert.ErrorIs(t, uc.RequireAdmin(ctx, "u1"), domainauth.ErrPermission)
	})

	t.Run("inactive admin is denied", func(t *testing.T) {
		repo := newFakeMemberRepo()
		repo.add(member("admin1", "admin", 2, false))
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})
		assert.ErrorIs(t, uc.RequireAdmin(ctx, "admin1"), domainauth.ErrPermission)
	})

	t.Run("unknown actor surfaces ErrNotFound", func(t *testing.T) {
		repo := newFakeMemberRepo()
		uc := NewAuthUseCase(repo, &fakeRoleRepo{}, &fakeHasher{}, &fakeTokenService{})
		assert.ErrorIs(t, uc.RequireAdmin(ctx, "ghost"), domainauth.ErrNotFound)
	})
}
