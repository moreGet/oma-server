package agentregistryapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// --- 페이크 ---

// fakeRepo 는 (owner, name) 업서트 시맨틱을 인메모리로 재현한다.
type fakeRepo struct {
	byID map[string]domainagentregistry.Agent
}

var _ domainagentregistry.Repository = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: map[string]domainagentregistry.Agent{}} }

func (r *fakeRepo) Upsert(_ context.Context, a domainagentregistry.Agent) (domainagentregistry.Agent, error) {
	for id, cur := range r.byID {
		if cur.OwnerID == a.OwnerID && cur.Name == a.Name {
			a.ID, a.CreatedAt = id, cur.CreatedAt // 기존 id/created_at 유지
			r.byID[id] = a
			return a, nil
		}
	}
	r.byID[a.ID] = a
	return a, nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (domainagentregistry.Agent, error) {
	a, ok := r.byID[id]
	if !ok {
		return domainagentregistry.Agent{}, domainagentregistry.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) Touch(_ context.Context, id, ownerID string, now time.Time) error {
	a, ok := r.byID[id]
	if !ok || a.OwnerID != ownerID {
		return domainagentregistry.ErrNotFound
	}
	a.LastHeartbeatAt, a.UpdatedAt = now, now
	r.byID[id] = a
	return nil
}

func (r *fakeRepo) Delete(_ context.Context, id, ownerID string) error {
	a, ok := r.byID[id]
	if !ok || a.OwnerID != ownerID {
		return domainagentregistry.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeRepo) DeleteByID(_ context.Context, id string) error {
	if _, ok := r.byID[id]; !ok {
		return domainagentregistry.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeRepo) DeleteHeartbeatBefore(_ context.Context, cutoff time.Time) (int, error) {
	n := 0
	for id, a := range r.byID {
		if a.LastHeartbeatAt.Before(cutoff) {
			delete(r.byID, id)
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) List(_ context.Context, f domainagentregistry.Filter) ([]domainagentregistry.Agent, error) {
	var out []domainagentregistry.Agent
	for _, a := range r.byID {
		if f.OwnerID != "" && a.OwnerID != f.OwnerID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// fakeGate 는 admin 게이트 페이크다.
type fakeGate struct{ err error }

func (g *fakeGate) RequireAdmin(context.Context, string) error { return g.err }

// fakeKeyRepo 는 A2A 키 저장 페이크다.
type fakeKeyRepo struct {
	stored *domainagentregistry.A2AKey
	saves  int
}

var _ domainagentregistry.KeyRepository = (*fakeKeyRepo)(nil)

func (r *fakeKeyRepo) GetActive(context.Context) (domainagentregistry.A2AKey, error) {
	if r.stored == nil {
		return domainagentregistry.A2AKey{}, domainagentregistry.ErrNotFound
	}
	return *r.stored, nil
}

func (r *fakeKeyRepo) Save(_ context.Context, k domainagentregistry.A2AKey) error {
	r.stored, r.saves = &k, r.saves+1
	return nil
}

// fakeSigner 는 서명 페이크다(발급 토큰 내용은 crypto 어댑터 테스트에서 실검증).
type fakeSigner struct {
	minted []domainagentregistry.A2AClaims
}

var _ domainagentregistry.TokenSigner = (*fakeSigner)(nil)

func (s *fakeSigner) GenerateKey() (domainagentregistry.A2AKey, error) {
	return domainagentregistry.A2AKey{KID: "kid-1", PrivateKeyPEM: "PRIV", PublicKeyPEM: "PUB"}, nil
}

func (s *fakeSigner) Mint(_ domainagentregistry.A2AKey, c domainagentregistry.A2AClaims) (string, error) {
	s.minted = append(s.minted, c)
	return "signed-token", nil
}

// fakeCipher 는 접두사 마킹으로 암복호화를 흉내낸다.
type fakeCipher struct{}

var _ domainagentregistry.Cipher = (*fakeCipher)(nil)

func (fakeCipher) Encrypt(plain string) (string, error) { return "enc:" + plain, nil }
func (fakeCipher) Decrypt(enc string) (string, error) {
	if len(enc) < 4 || enc[:4] != "enc:" {
		return "", errors.New("not encrypted")
	}
	return enc[4:], nil
}

// --- 헬퍼 ---

func newTestService(repo *fakeRepo) *Service {
	return NewService(repo, &fakeGate{}, 15*time.Second, 45*time.Second)
}

func register(t *testing.T, s *Service, owner, name string, caps ...string) domainagentregistry.Agent {
	t.Helper()
	a, err := s.Register(context.Background(), domainagentregistry.RegisterCommand{
		OwnerID: owner, Name: name, EndpointURL: "http://10.0.0.5:8080", Capabilities: caps,
	})
	require.NoError(t, err)
	return a
}

// --- 테스트 ---

func TestRegisterUpsertReusesAgentID(t *testing.T) {
	s := newTestService(newFakeRepo())

	first := register(t, s, "m1", "reviewer", "code-review")
	assert.NotEmpty(t, first.ID)
	assert.Equal(t, domainagentregistry.StatusOnline, first.Status, "등록 직후는 online")

	// 같은 (owner, name) 재등록 → 같은 agent_id 유지 + endpoint 갱신.
	again, err := s.Register(context.Background(), domainagentregistry.RegisterCommand{
		OwnerID: "m1", Name: "reviewer", EndpointURL: "http://10.0.0.9:9090",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "재등록은 기존 agent_id 유지")
	assert.Equal(t, "http://10.0.0.9:9090", again.EndpointURL)

	// 다른 owner 의 같은 name 은 별개 레코드.
	other := register(t, s, "m2", "reviewer")
	assert.NotEqual(t, first.ID, other.ID)
}

func TestHeartbeatOwnerScope(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	a := register(t, s, "m1", "reviewer")

	require.NoError(t, s.Heartbeat(context.Background(), a.ID, "m1"))

	// 존재하지만 소유자 불일치 → ErrForbidden(핸들러가 404 로 은닉 매핑).
	err := s.Heartbeat(context.Background(), a.ID, "m2")
	assert.ErrorIs(t, err, domainagentregistry.ErrForbidden)

	// 없는 id → ErrNotFound.
	err = s.Heartbeat(context.Background(), "nope", "m1")
	assert.ErrorIs(t, err, domainagentregistry.ErrNotFound)
}

func TestDeregisterOwnerScope(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	a := register(t, s, "m1", "reviewer")

	assert.ErrorIs(t, s.Deregister(context.Background(), a.ID, "m2"), domainagentregistry.ErrForbidden)
	require.NoError(t, s.Deregister(context.Background(), a.ID, "m1"))
	assert.ErrorIs(t, s.Deregister(context.Background(), a.ID, "m1"), domainagentregistry.ErrNotFound)
}

func TestDiscoverFiltersAndLiveness(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	online := register(t, s, "m1", "online-agent", "code-review")
	staleA := register(t, s, "m1", "stale-agent", "code-review")
	offline := register(t, s, "m2", "offline-agent", "vision")

	// heartbeat 시각을 조작해 생존성 전이를 만든다(online: 방금 / stale: 60s 전 / offline: 10분 전).
	touch := func(id string, ago time.Duration) {
		a := repo.byID[id]
		a.LastHeartbeatAt = now.Add(-ago)
		repo.byID[id] = a
	}
	touch(online.ID, 0)
	touch(staleA.ID, 60*time.Second)  // 45s ≤ 60s < 135s → stale
	touch(offline.ID, 10*time.Minute) // ≥ 135s → offline

	t.Run("기본은 online+stale 만(offline 은닉)", func(t *testing.T) {
		got, err := s.Discover(context.Background(), domainagentregistry.Filter{})
		require.NoError(t, err)
		ids := map[string]domainagentregistry.Status{}
		for _, a := range got {
			ids[a.ID] = a.Status
		}
		assert.Len(t, ids, 2)
		assert.Equal(t, domainagentregistry.StatusOnline, ids[online.ID])
		assert.Equal(t, domainagentregistry.StatusStale, ids[staleA.ID])
	})

	t.Run("status=online 은 online 만", func(t *testing.T) {
		got, err := s.Discover(context.Background(), domainagentregistry.Filter{Status: "online"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, online.ID, got[0].ID)
	})

	t.Run("status=offline 조회 가능", func(t *testing.T) {
		got, err := s.Discover(context.Background(), domainagentregistry.Filter{Status: "offline"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, offline.ID, got[0].ID)
	})

	t.Run("capability + exclude_self 조합", func(t *testing.T) {
		got, err := s.Discover(context.Background(), domainagentregistry.Filter{
			Capability: "code-review", ExcludeID: online.ID,
		})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, staleA.ID, got[0].ID)
	})

	t.Run("잘못된 status 값은 400 검증 에러", func(t *testing.T) {
		_, err := s.Discover(context.Background(), domainagentregistry.Filter{Status: "zombie"})
		var ve *domainagentregistry.ErrValidation
		assert.ErrorAs(t, err, &ve)
	})
}

func TestAdminListAndDelete(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	a := register(t, s, "m1", "reviewer")

	t.Run("admin 게이트 통과 시 offline 포함 전체", func(t *testing.T) {
		got, err := s.AdminList(context.Background(), "admin")
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("게이트 거부 전파", func(t *testing.T) {
		denied := NewService(repo, &fakeGate{err: errors.New("denied")}, 0, 0)
		_, err := denied.AdminList(context.Background(), "user")
		assert.Error(t, err)
	})

	t.Run("강제 해제는 소유자 무관", func(t *testing.T) {
		require.NoError(t, s.AdminDelete(context.Background(), "admin", a.ID))
		assert.ErrorIs(t, s.AdminDelete(context.Background(), "admin", a.ID), domainagentregistry.ErrNotFound)
	})
}

// TestEnableBrokerBootstrap 은 키 bootstrap(없으면 생성·암호화 저장, 있으면 복호화 재사용)을 검증한다.
func TestEnableBrokerBootstrap(t *testing.T) {
	keys := &fakeKeyRepo{}
	signer := &fakeSigner{}

	t.Run("활성 키 없으면 생성 + 암호화 저장", func(t *testing.T) {
		s := newTestService(newFakeRepo())
		require.NoError(t, s.EnableBroker(context.Background(), keys, signer, fakeCipher{}, 0))
		require.NotNil(t, keys.stored)
		assert.Equal(t, "enc:PRIV", keys.stored.PrivateKeyPEM, "저장본은 암호문")
		assert.True(t, keys.stored.Active)

		kid, alg, pub := s.PublicKey()
		assert.Equal(t, "kid-1", kid)
		assert.Equal(t, "ES256", alg)
		assert.Equal(t, "PUB", pub)
	})

	t.Run("재기동 시 기존 키 복호화 재사용(재생성 없음)", func(t *testing.T) {
		s := newTestService(newFakeRepo())
		require.NoError(t, s.EnableBroker(context.Background(), keys, signer, fakeCipher{}, 0))
		assert.Equal(t, 1, keys.saves, "이미 있으면 Save 재호출 없음")
		kid, _, _ := s.PublicKey()
		assert.Equal(t, "kid-1", kid)
	})
}

// TestMintToken 은 §공유 계약 클레임 구성·대상 404 를 검증한다(서명 실검증은 crypto 어댑터 테스트).
func TestMintToken(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	signer := &fakeSigner{}
	require.NoError(t, s.EnableBroker(context.Background(), &fakeKeyRepo{}, signer, fakeCipher{}, 120*time.Second))

	caller := register(t, s, "m1", "caller")
	target := register(t, s, "m2", "target")

	t.Run("클레임 구성(iss/sub/cid/aud/exp)", func(t *testing.T) {
		tok, err := s.MintToken(context.Background(), "m1", target.ID)
		require.NoError(t, err)
		assert.Equal(t, "signed-token", tok.Token)
		assert.Equal(t, 120*time.Second, tok.ExpiresIn)
		assert.Equal(t, target.ID, tok.AudienceAgentID)

		require.Len(t, signer.minted, 1)
		c := signer.minted[0]
		assert.Equal(t, domainagentregistry.A2AIssuer, c.Issuer)
		assert.Equal(t, "m1", c.Subject)
		assert.Equal(t, caller.ID, c.CallerAgentID, "소유 에이전트가 1개면 cid 특정")
		assert.Equal(t, target.ID, c.Audience)
		assert.Equal(t, now.Add(120*time.Second), c.ExpiresAt)
		assert.NotEmpty(t, c.JTI)
	})

	t.Run("호출자 소유 에이전트가 여럿이면 cid 생략", func(t *testing.T) {
		register(t, s, "m1", "second-agent")
		_, err := s.MintToken(context.Background(), "m1", target.ID)
		require.NoError(t, err)
		assert.Empty(t, signer.minted[len(signer.minted)-1].CallerAgentID)
	})

	t.Run("대상 미존재 404", func(t *testing.T) {
		_, err := s.MintToken(context.Background(), "m1", "nope")
		assert.ErrorIs(t, err, domainagentregistry.ErrNotFound)
	})

	t.Run("브로커 미구성 시 내부 오류", func(t *testing.T) {
		bare := newTestService(repo)
		_, err := bare.MintToken(context.Background(), "m1", target.ID)
		assert.Error(t, err)
	})
}

// TestSweeperRetention 은 sweeper 커토프가 offline 문턱보다 보수적인지(24h) 확인한다.
func TestSweeperRetention(t *testing.T) {
	repo := newFakeRepo()
	s := newTestService(repo)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	old := register(t, s, "m1", "old")
	fresh := register(t, s, "m1", "fresh")
	a := repo.byID[old.ID]
	a.LastHeartbeatAt = now.Add(-25 * time.Hour)
	repo.byID[old.ID] = a

	n, err := repo.DeleteHeartbeatBefore(context.Background(), now.Add(-sweepOfflineRetention))
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, ok := repo.byID[fresh.ID]
	assert.True(t, ok, "최근 heartbeat 레코드는 보존")
}
