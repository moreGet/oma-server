package sessionapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

type fakeSettingsRepo struct{ s domainproject.Settings }

func (r *fakeSettingsRepo) Get(context.Context) (domainproject.Settings, error) { return r.s, nil }
func (r *fakeSettingsRepo) Save(_ context.Context, s domainproject.Settings) error {
	r.s = s
	return nil
}

type fakeLimits struct{ m map[string]int }

func (r *fakeLimits) Get(_ context.Context, id string) (int, error) { return r.m[id], nil }
func (r *fakeLimits) Set(_ context.Context, id string, max int) error {
	r.m[id] = max
	return nil
}
func (r *fakeLimits) ByIDs(_ context.Context, ids []string) (map[string]int, error) {
	out := make(map[string]int, len(ids))
	for _, id := range ids {
		if v, ok := r.m[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type fakeStore struct{}

func (fakeStore) Save(context.Context, string, []byte) error { return nil }

type fakeFactory struct{ pingErr error }

func (f *fakeFactory) Build(domainproject.Settings) (domainproject.ContentStore, error) {
	return fakeStore{}, nil
}
func (f *fakeFactory) Ping(context.Context, domainproject.Settings) error { return f.pingErr }

type idCipher struct{}

func (idCipher) Encrypt(s string) (string, error) { return s, nil }
func (idCipher) Decrypt(s string) (string, error) { return s, nil }

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

func newMgr(t *testing.T, init domainproject.Settings) (*Manager, *fakeSettingsRepo, *fakeLimits) {
	repo := &fakeSettingsRepo{s: init}
	limits := &fakeLimits{m: map[string]int{}}
	m, err := NewManager(repo, limits, &fakeFactory{}, idCipher{}, allowGate{})
	require.NoError(t, err)
	return m, repo, limits
}

func TestManager_EffectiveMaxSessions(t *testing.T) {
	m, _, limits := newMgr(t, domainproject.Settings{Backend: domainproject.BackendDB, DefaultMaxSessions: 50})
	// 오버라이드 없음 → 전역 기본.
	v, err := m.EffectiveMaxSessions(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, 50, v)
	// 멤버 오버라이드 우선.
	limits.m["u1"] = 10
	v, _ = m.EffectiveMaxSessions(context.Background(), "u1")
	assert.Equal(t, 10, v)
}

func TestManager_UpdateSettingsReloadsAndMasks(t *testing.T) {
	m, repo, _ := newMgr(t, domainproject.DefaultSettings())
	require.Equal(t, 0, m.DefaultMaxSessions())

	require.NoError(t, m.UpdateSettings(context.Background(), domainproject.UpdateSettingsCommand{
		ActorID: "admin",
		Settings: domainproject.Settings{
			Backend: domainproject.BackendS3, S3Endpoint: "minio:9000", S3Bucket: "b", S3AccessKey: "ak",
			S3SecretKey: "topsecret", DefaultMaxSessions: 7,
		},
	}))
	assert.Equal(t, 7, m.DefaultMaxSessions(), "reload 후 전역 캡 반영")
	assert.Equal(t, "topsecret", repo.s.S3SecretKey, "identity cipher 라 그대로 저장")

	// GetSettings 는 시크릿 마스킹 + 존재 여부.
	got, hasSecret, err := m.GetSettings(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, hasSecret)
	assert.Empty(t, got.S3SecretKey)

	// 시크릿 공백 재저장 → 기존 보존.
	require.NoError(t, m.UpdateSettings(context.Background(), domainproject.UpdateSettingsCommand{
		ActorID:  "admin",
		Settings: domainproject.Settings{Backend: domainproject.BackendS3, S3Endpoint: "minio:9000", S3Bucket: "b", S3AccessKey: "ak", S3SecretKey: ""},
	}))
	assert.Equal(t, "topsecret", repo.s.S3SecretKey)
}

func TestManager_UpdateSettingsRejectsInvalid(t *testing.T) {
	m, _, _ := newMgr(t, domainproject.DefaultSettings())
	err := m.UpdateSettings(context.Background(), domainproject.UpdateSettingsCommand{
		ActorID:  "admin",
		Settings: domainproject.Settings{Backend: domainproject.BackendFile}, // 디렉터리 없음
	})
	var ve *domainproject.ErrValidation
	assert.ErrorAs(t, err, &ve)
}

func TestManager_SetMemberLimitAndTest(t *testing.T) {
	m, _, limits := newMgr(t, domainproject.DefaultSettings())
	require.NoError(t, m.SetMemberLimit(context.Background(), "admin", "u1", 5))
	assert.Equal(t, 5, limits.m["u1"])
	require.NoError(t, m.TestConnection(context.Background(), "admin"))
}
