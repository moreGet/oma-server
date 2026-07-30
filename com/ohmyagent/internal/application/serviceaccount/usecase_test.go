package serviceaccountapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

var errDB = errors.New("db exploded")

// --- 페이크 ---

// fakeRepo 는 domainserviceaccount.Repository 페이크다(호출 기록 + 주입 가능한 오류/데이터).
// best-effort touch 는 background 고루틴에서 호출될 수 있으므로 뮤텍스로 race-safe 하게 보호한다.
type fakeRepo struct {
	mu sync.Mutex

	accounts map[string]domainserviceaccount.ServiceAccount
	keys     map[string]domainserviceaccount.ServiceAccountKey // token_hash → key
	acctByID map[string]domainserviceaccount.ServiceAccount    // FindKeyForAuth 조립용

	saveAccountErr    error
	findAccountErr    error // FindAccountByID 강제 오류(nil 이면 map 조회)
	findKeyForAuthErr error // FindKeyForAuth 강제 오류(DB오류 주입용)

	// 호출 기록
	savedAccounts    []domainserviceaccount.ServiceAccount
	savedKeys        []domainserviceaccount.ServiceAccountKey
	revokeAccountIDs []string
	revokeKeysAcctID []string
	touchedKeyIDs    []string
}

var _ domainserviceaccount.Repository = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		accounts: map[string]domainserviceaccount.ServiceAccount{},
		keys:     map[string]domainserviceaccount.ServiceAccountKey{},
		acctByID: map[string]domainserviceaccount.ServiceAccount{},
	}
}

func (r *fakeRepo) SaveAccount(_ context.Context, a domainserviceaccount.ServiceAccount) error {
	if r.saveAccountErr != nil {
		return r.saveAccountErr
	}
	r.savedAccounts = append(r.savedAccounts, a)
	r.accounts[a.ID] = a
	return nil
}

func (r *fakeRepo) FindAccountByID(_ context.Context, id string) (domainserviceaccount.ServiceAccount, error) {
	if r.findAccountErr != nil {
		return domainserviceaccount.ServiceAccount{}, r.findAccountErr
	}
	a, ok := r.accounts[id]
	if !ok {
		return domainserviceaccount.ServiceAccount{}, domainserviceaccount.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) ListAccounts(_ context.Context) ([]domainserviceaccount.ServiceAccount, error) {
	out := make([]domainserviceaccount.ServiceAccount, 0, len(r.accounts))
	for _, a := range r.accounts {
		if !a.Revoked() {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *fakeRepo) RevokeAccount(_ context.Context, id string, _ time.Time) error {
	r.revokeAccountIDs = append(r.revokeAccountIDs, id)
	a, ok := r.accounts[id]
	if !ok || a.Revoked() {
		return domainserviceaccount.ErrNotFound
	}
	return nil
}

func (r *fakeRepo) SaveKey(_ context.Context, k domainserviceaccount.ServiceAccountKey) error {
	r.savedKeys = append(r.savedKeys, k)
	r.keys[k.TokenHash] = k
	return nil
}

func (r *fakeRepo) ListKeysByAccount(_ context.Context, accountID string) ([]domainserviceaccount.ServiceAccountKey, error) {
	var out []domainserviceaccount.ServiceAccountKey
	for _, k := range r.keys {
		if k.AccountID == accountID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r *fakeRepo) ListKeysByAccounts(ctx context.Context, accountIDs []string) (map[string][]domainserviceaccount.ServiceAccountKey, error) {
	out := make(map[string][]domainserviceaccount.ServiceAccountKey, len(accountIDs))
	for _, id := range accountIDs {
		keys, err := r.ListKeysByAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if keys != nil {
			out[id] = keys
		}
	}
	return out, nil
}

func (r *fakeRepo) RevokeKey(_ context.Context, accountID, keyID string, _ time.Time) error {
	for _, k := range r.keys {
		if k.ID == keyID && k.AccountID == accountID {
			return nil
		}
	}
	return domainserviceaccount.ErrKeyNotFound
}

func (r *fakeRepo) RevokeKeysByAccount(_ context.Context, accountID string, _ time.Time) error {
	r.revokeKeysAcctID = append(r.revokeKeysAcctID, accountID)
	return nil
}

func (r *fakeRepo) FindKeyForAuth(_ context.Context, tokenHash string) (domainserviceaccount.ServiceAccountKey, domainserviceaccount.ServiceAccount, error) {
	if r.findKeyForAuthErr != nil {
		return domainserviceaccount.ServiceAccountKey{}, domainserviceaccount.ServiceAccount{}, r.findKeyForAuthErr
	}
	k, ok := r.keys[tokenHash]
	if !ok {
		return domainserviceaccount.ServiceAccountKey{}, domainserviceaccount.ServiceAccount{}, domainserviceaccount.ErrKeyNotFound
	}
	a := r.acctByID[k.AccountID]
	return k, a, nil
}

func (r *fakeRepo) TouchKeyLastUsed(_ context.Context, keyID string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchedKeyIDs = append(r.touchedKeyIDs, keyID)
	return nil
}

// fakeHasher 는 domainserviceaccount.TokenHasher 페이크다(결정적 해시).
type fakeHasher struct {
	plain   string
	hash    string
	newErr  error
	hashArg string
}

var _ domainserviceaccount.TokenHasher = (*fakeHasher)(nil)

func (h *fakeHasher) NewToken() (string, string, error) {
	if h.newErr != nil {
		return "", "", h.newErr
	}
	return h.plain, h.hash, nil
}

func (h *fakeHasher) Hash(raw string) string {
	h.hashArg = raw
	return "hash(" + raw + ")"
}

// fakeGate 는 accessGate 페이크다.
type fakeGate struct {
	adminErr  error // RequireAdmin 결과
	memberErr error // RequireActiveMember 결과
	member    domainauth.Member
}

func (g *fakeGate) RequireAdmin(_ context.Context, _ string) error { return g.adminErr }
func (g *fakeGate) RequireActiveMember(_ context.Context, _ string) (domainauth.Member, error) {
	return g.member, g.memberErr
}

// newService 는 결정적 now 를 주입한 유스케이스를 만든다(같은 패키지라 비공개 필드 접근 가능).
func newService(repo *fakeRepo, hasher *fakeHasher, gate *fakeGate) (*Service, time.Time) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	s := NewService(repo, hasher, gate)
	s.now = func() time.Time { return now }
	return s, now
}

// --- Authenticate ---

func TestAuthenticate(t *testing.T) {
	t.Run("정상 → Claims{MemberID:sa.ID, Username:sa.Name, Level:User}", func(t *testing.T) {
		repo := newFakeRepo()
		acct := domainserviceaccount.ServiceAccount{ID: "sa-1", Name: "bot"}
		repo.acctByID["sa-1"] = acct
		hasher := &fakeHasher{}
		s, now := newService(repo, hasher, &fakeGate{})
		// 최근 사용으로 세팅 → best-effort touch 스킵(고루틴 없음, 결정적).
		repo.keys["hash(oma_sa_tok)"] = domainserviceaccount.ServiceAccountKey{
			ID: "k-1", AccountID: "sa-1", TokenHash: "hash(oma_sa_tok)", LastUsedAt: now,
		}

		claims, err := s.Authenticate(context.Background(), "oma_sa_tok")
		require.NoError(t, err)
		assert.Equal(t, "sa-1", claims.MemberID, "sa.ID 를 member_id 자리에")
		assert.Equal(t, "bot", claims.Username)
		assert.Equal(t, domainauth.RoleLevelUser, claims.Level, "최소권한 user 고정")
		assert.Equal(t, "oma_sa_tok", hasher.hashArg, "raw 토큰을 그대로 해시")
		assert.Empty(t, repo.touchedKeyIDs, "최근 사용분은 touch 스킵")
	})

	t.Run("폐기 키 → ErrInvalidToken", func(t *testing.T) {
		repo := newFakeRepo()
		repo.acctByID["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1"}
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		repo.keys["hash(oma_sa_x)"] = domainserviceaccount.ServiceAccountKey{
			ID: "k-1", AccountID: "sa-1", TokenHash: "hash(oma_sa_x)", RevokedAt: now.Add(-time.Hour),
		}
		_, err := s.Authenticate(context.Background(), "oma_sa_x")
		assert.ErrorIs(t, err, domainauth.ErrInvalidToken)
	})

	t.Run("만료 키(경계 now==ExpiresAt) → ErrInvalidToken", func(t *testing.T) {
		repo := newFakeRepo()
		repo.acctByID["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1"}
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		repo.keys["hash(oma_sa_x)"] = domainserviceaccount.ServiceAccountKey{
			ID: "k-1", AccountID: "sa-1", TokenHash: "hash(oma_sa_x)", ExpiresAt: now,
		}
		_, err := s.Authenticate(context.Background(), "oma_sa_x")
		assert.ErrorIs(t, err, domainauth.ErrInvalidToken)
	})

	t.Run("폐기된 계정의 키 → ErrInvalidToken", func(t *testing.T) {
		repo := newFakeRepo()
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		repo.acctByID["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1", RevokedAt: now.Add(-time.Hour)}
		repo.keys["hash(oma_sa_x)"] = domainserviceaccount.ServiceAccountKey{
			ID: "k-1", AccountID: "sa-1", TokenHash: "hash(oma_sa_x)", LastUsedAt: now,
		}
		_, err := s.Authenticate(context.Background(), "oma_sa_x")
		assert.ErrorIs(t, err, domainauth.ErrInvalidToken)
	})

	t.Run("미존재 해시 → ErrInvalidToken", func(t *testing.T) {
		repo := newFakeRepo()
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		_, err := s.Authenticate(context.Background(), "oma_sa_unknown")
		assert.ErrorIs(t, err, domainauth.ErrInvalidToken)
	})

	t.Run("FindKeyForAuth DB오류 → ErrInvalidToken(5xx 아님, §2C 핵심)", func(t *testing.T) {
		repo := newFakeRepo()
		repo.findKeyForAuthErr = errDB
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		_, err := s.Authenticate(context.Background(), "oma_sa_x")
		assert.ErrorIs(t, err, domainauth.ErrInvalidToken, "DB오류도 401 로 수렴")
		assert.NotErrorIs(t, err, errDB, "인프라 오류 원문을 전파하지 않음")
	})
}

// --- CreateAccount ---

func TestCreateAccount(t *testing.T) {
	baseCmd := func() domainserviceaccount.CreateAccountCommand {
		return domainserviceaccount.CreateAccountCommand{ActorID: "admin-1", Name: "bot", OwnerMemberID: "owner-1"}
	}

	t.Run("non-admin actor → 게이트 ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		_, err := s.CreateAccount(context.Background(), baseCmd())
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("미존재 owner(ErrNotFound) → ErrValidation", func(t *testing.T) {
		gate := &fakeGate{memberErr: domainauth.ErrNotFound}
		s, _ := newService(newFakeRepo(), &fakeHasher{}, gate)
		_, err := s.CreateAccount(context.Background(), baseCmd())
		var ve *domainserviceaccount.ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "owner_member_id does not exist", ve.Msg)
	})

	t.Run("비활성 owner(ErrPermission from RequireActiveMember) → ErrValidation", func(t *testing.T) {
		gate := &fakeGate{memberErr: domainauth.ErrPermission}
		s, _ := newService(newFakeRepo(), &fakeHasher{}, gate)
		_, err := s.CreateAccount(context.Background(), baseCmd())
		var ve *domainserviceaccount.ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "owner_member_id is not an active member", ve.Msg)
	})

	t.Run("입력 검증 실패(name 누락) → ErrValidation", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{})
		cmd := baseCmd()
		cmd.Name = "   "
		_, err := s.CreateAccount(context.Background(), cmd)
		var ve *domainserviceaccount.ErrValidation
		require.ErrorAs(t, err, &ve)
	})

	t.Run("정상 생성 → 필드 채움 + Save 호출", func(t *testing.T) {
		repo := newFakeRepo()
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		acct, err := s.CreateAccount(context.Background(), baseCmd())
		require.NoError(t, err)
		assert.NotEmpty(t, acct.ID)
		assert.Equal(t, "bot", acct.Name)
		assert.Equal(t, "owner-1", acct.OwnerMemberID)
		assert.Equal(t, "admin-1", acct.CreatedBy)
		assert.Equal(t, now.Truncate(time.Second), acct.CreatedAt)
		assert.Equal(t, now.Truncate(time.Second), acct.UpdatedAt)
		require.Len(t, repo.savedAccounts, 1)
		assert.Equal(t, acct.ID, repo.savedAccounts[0].ID)
	})
}

// --- IssueKey ---

func TestIssueKey(t *testing.T) {
	seedAccount := func(repo *fakeRepo, a domainserviceaccount.ServiceAccount) {
		repo.accounts[a.ID] = a
	}

	t.Run("non-admin → 게이트 ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		_, _, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{AccountID: "sa-1"})
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("미존재 계정 → ErrNotFound", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{})
		_, _, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{ActorID: "admin", AccountID: "nope"})
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})

	t.Run("폐기 계정 → ErrNotFound", func(t *testing.T) {
		repo := newFakeRepo()
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		seedAccount(repo, domainserviceaccount.ServiceAccount{ID: "sa-1", RevokedAt: now.Add(-time.Hour)})
		_, _, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{ActorID: "admin", AccountID: "sa-1"})
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})

	t.Run("과거 expires_at → ErrValidation", func(t *testing.T) {
		repo := newFakeRepo()
		s, now := newService(repo, &fakeHasher{}, &fakeGate{})
		seedAccount(repo, domainserviceaccount.ServiceAccount{ID: "sa-1"})
		_, _, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{
			ActorID: "admin", AccountID: "sa-1", ExpiresAt: now.Add(-time.Second),
		})
		var ve *domainserviceaccount.ErrValidation
		require.ErrorAs(t, err, &ve)
	})

	t.Run("무기한(0) 정상 → 평문 반환, 저장은 해시만", func(t *testing.T) {
		repo := newFakeRepo()
		hasher := &fakeHasher{plain: "oma_sa_PLAINTEXT", hash: "sha256hex"}
		s, now := newService(repo, hasher, &fakeGate{})
		seedAccount(repo, domainserviceaccount.ServiceAccount{ID: "sa-1"})

		key, plain, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{
			ActorID: "admin", AccountID: "sa-1",
		})
		require.NoError(t, err)
		assert.Equal(t, "oma_sa_PLAINTEXT", plain, "평문은 반환값으로만 노출")
		assert.NotEmpty(t, key.ID)
		assert.Equal(t, "sa-1", key.AccountID)
		assert.Equal(t, "sha256hex", key.TokenHash)
		assert.True(t, key.ExpiresAt.IsZero(), "무기한")
		assert.Equal(t, now.Truncate(time.Second), key.CreatedAt)
		require.Len(t, repo.savedKeys, 1)
		assert.Equal(t, "sha256hex", repo.savedKeys[0].TokenHash, "저장은 해시만")
		assert.NotContains(t, repo.savedKeys[0].TokenHash, "PLAINTEXT", "평문 미저장")
	})

	t.Run("미래 expires_at 정상 → 반영", func(t *testing.T) {
		repo := newFakeRepo()
		hasher := &fakeHasher{plain: "oma_sa_p", hash: "h"}
		s, now := newService(repo, hasher, &fakeGate{})
		seedAccount(repo, domainserviceaccount.ServiceAccount{ID: "sa-1"})
		exp := now.Add(90 * 24 * time.Hour)
		key, _, err := s.IssueKey(context.Background(), domainserviceaccount.IssueKeyCommand{
			ActorID: "admin", AccountID: "sa-1", ExpiresAt: exp,
		})
		require.NoError(t, err)
		assert.Equal(t, exp, key.ExpiresAt)
	})
}

// --- DeleteAccount ---

func TestDeleteAccount(t *testing.T) {
	t.Run("non-admin → 게이트 ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		err := s.DeleteAccount(context.Background(), "actor", "sa-1")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("정상 → RevokeAccount + RevokeKeysByAccount 둘 다 호출", func(t *testing.T) {
		repo := newFakeRepo()
		repo.accounts["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1"}
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		require.NoError(t, s.DeleteAccount(context.Background(), "admin", "sa-1"))
		assert.Equal(t, []string{"sa-1"}, repo.revokeAccountIDs)
		assert.Equal(t, []string{"sa-1"}, repo.revokeKeysAcctID, "딸린 키 연쇄 폐기")
	})

	t.Run("미존재 계정 → ErrNotFound, 키 폐기 미호출", func(t *testing.T) {
		repo := newFakeRepo()
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		err := s.DeleteAccount(context.Background(), "admin", "nope")
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
		assert.Empty(t, repo.revokeKeysAcctID, "계정 폐기 실패 시 키 폐기 미실행")
	})
}

// --- RevokeKey / ListKeys / ListAccounts (admin 게이트 + 위임) ---

func TestRevokeKey(t *testing.T) {
	t.Run("non-admin → ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		err := s.RevokeKey(context.Background(), "actor", "sa-1", "k-1")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("타 계정 키 id → ErrKeyNotFound(sa_id 스코프)", func(t *testing.T) {
		repo := newFakeRepo()
		repo.keys["h"] = domainserviceaccount.ServiceAccountKey{ID: "k-1", AccountID: "sa-1", TokenHash: "h"}
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		err := s.RevokeKey(context.Background(), "admin", "sa-OTHER", "k-1")
		assert.ErrorIs(t, err, domainserviceaccount.ErrKeyNotFound)
	})

	t.Run("정상 위임", func(t *testing.T) {
		repo := newFakeRepo()
		repo.keys["h"] = domainserviceaccount.ServiceAccountKey{ID: "k-1", AccountID: "sa-1", TokenHash: "h"}
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		require.NoError(t, s.RevokeKey(context.Background(), "admin", "sa-1", "k-1"))
	})
}

func TestListKeys(t *testing.T) {
	t.Run("non-admin → ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		_, err := s.ListKeys(context.Background(), "actor", "sa-1")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("미존재 계정 → ErrNotFound", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{})
		_, err := s.ListKeys(context.Background(), "admin", "nope")
		assert.ErrorIs(t, err, domainserviceaccount.ErrNotFound)
	})

	t.Run("정상 위임", func(t *testing.T) {
		repo := newFakeRepo()
		repo.accounts["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1"}
		repo.keys["h"] = domainserviceaccount.ServiceAccountKey{ID: "k-1", AccountID: "sa-1", TokenHash: "h"}
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		keys, err := s.ListKeys(context.Background(), "admin", "sa-1")
		require.NoError(t, err)
		require.Len(t, keys, 1)
		assert.Equal(t, "k-1", keys[0].ID)
	})
}

func TestListAccounts(t *testing.T) {
	t.Run("non-admin → ErrPermission", func(t *testing.T) {
		s, _ := newService(newFakeRepo(), &fakeHasher{}, &fakeGate{adminErr: domainauth.ErrPermission})
		_, err := s.ListAccounts(context.Background(), "actor")
		assert.ErrorIs(t, err, domainauth.ErrPermission)
	})

	t.Run("정상 → 계정 + 키 메타 조립(활성만)", func(t *testing.T) {
		repo := newFakeRepo()
		repo.accounts["sa-1"] = domainserviceaccount.ServiceAccount{ID: "sa-1", Name: "bot"}
		repo.keys["h"] = domainserviceaccount.ServiceAccountKey{ID: "k-1", AccountID: "sa-1", TokenHash: "h"}
		s, _ := newService(repo, &fakeHasher{}, &fakeGate{})
		out, err := s.ListAccounts(context.Background(), "admin")
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "sa-1", out[0].Account.ID)
		require.Len(t, out[0].Keys, 1)
		assert.Equal(t, "k-1", out[0].Keys[0].ID)
	})
}
