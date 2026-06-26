package transcriptapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

type fakeSettingsRepo struct{ s domaintranscript.Settings }

func (r *fakeSettingsRepo) Get(context.Context) (domaintranscript.Settings, error) { return r.s, nil }
func (r *fakeSettingsRepo) Save(_ context.Context, s domaintranscript.Settings) error {
	r.s = s
	return nil
}

type fakeStore struct{}

func (fakeStore) Save(context.Context, domaintranscript.Transcript) error { return nil }

type fakeFactory struct {
	built   []domaintranscript.Settings
	pingErr error
}

func (f *fakeFactory) Build(s domaintranscript.Settings) (domaintranscript.Store, error) {
	f.built = append(f.built, s)
	return fakeStore{}, nil
}
func (f *fakeFactory) Ping(context.Context, domaintranscript.Settings) error { return f.pingErr }

// identityCipher: 평문=암호문(테스트용).
type identityCipher struct{}

func (identityCipher) Encrypt(s string) (string, error) { return s, nil }
func (identityCipher) Decrypt(s string) (string, error) { return s, nil }

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

func newManager(t *testing.T, init domaintranscript.Settings) (*Manager, *fakeSettingsRepo, *fakeFactory) {
	repo := &fakeSettingsRepo{s: init}
	fac := &fakeFactory{}
	m, err := NewManager(repo, fac, identityCipher{}, allowGate{})
	require.NoError(t, err)
	return m, repo, fac
}

func TestManager_EnabledTogglesWithSettings(t *testing.T) {
	m, _, _ := newManager(t, domaintranscript.DefaultSettings())
	assert.True(t, m.Enabled())

	require.NoError(t, m.UpdateSettings(context.Background(), domaintranscript.UpdateCommand{
		ActorID:  "admin",
		Settings: domaintranscript.Settings{Enabled: false, Backend: domaintranscript.BackendDB},
	}))
	assert.False(t, m.Enabled(), "비활성으로 갱신 시 Enabled 가 false 여야 함")
}

func TestManager_UpdateEncryptsSecretAndPreservesWhenBlank(t *testing.T) {
	m, repo, _ := newManager(t, domaintranscript.DefaultSettings())

	// S3 + secret 입력 → 저장(identity cipher 라 그대로) + 백엔드 전환.
	require.NoError(t, m.UpdateSettings(context.Background(), domaintranscript.UpdateCommand{
		ActorID: "admin",
		Settings: domaintranscript.Settings{
			Enabled: true, Backend: domaintranscript.BackendS3,
			S3Endpoint: "minio:9000", S3Bucket: "b", S3AccessKey: "ak", S3SecretKey: "topsecret",
		},
	}))
	assert.Equal(t, "topsecret", repo.s.S3SecretKey)

	// secret 비워 재저장 → 기존 값 보존.
	require.NoError(t, m.UpdateSettings(context.Background(), domaintranscript.UpdateCommand{
		ActorID: "admin",
		Settings: domaintranscript.Settings{
			Enabled: true, Backend: domaintranscript.BackendS3,
			S3Endpoint: "minio:9000", S3Bucket: "b", S3AccessKey: "ak", S3SecretKey: "",
		},
	}))
	assert.Equal(t, "topsecret", repo.s.S3SecretKey, "secret 공백 입력 시 기존 값 보존")

	// GetSettings 는 시크릿 마스킹 + 존재 여부 반환.
	got, hasSecret, err := m.GetSettings(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, hasSecret)
	assert.Empty(t, got.S3SecretKey, "응답에 시크릿 노출 금지")
}

func TestManager_UpdateRejectsInvalid(t *testing.T) {
	m, _, _ := newManager(t, domaintranscript.DefaultSettings())
	// file 백엔드인데 디렉터리 없음 → 검증 실패.
	err := m.UpdateSettings(context.Background(), domaintranscript.UpdateCommand{
		ActorID:  "admin",
		Settings: domaintranscript.Settings{Enabled: true, Backend: domaintranscript.BackendFile},
	})
	var ve *domaintranscript.ErrValidation
	assert.True(t, errors.As(err, &ve))
}
