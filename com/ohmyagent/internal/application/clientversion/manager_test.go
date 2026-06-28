package clientversionapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainclientversion "aiagent/com/ohmyagent/internal/domain/clientversion"
)

type fakeRepo struct{ s domainclientversion.Settings }

func (r *fakeRepo) Get(context.Context) (domainclientversion.Settings, error) { return r.s, nil }
func (r *fakeRepo) Save(_ context.Context, s domainclientversion.Settings) error {
	r.s = s
	return nil
}

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

type denyGate struct{}

var errForbidden = errors.New("forbidden")

func (denyGate) RequireAdmin(context.Context, string) error { return errForbidden }

func TestManager_UpdateNormalizesReloadsStampsAndProvides(t *testing.T) {
	repo := &fakeRepo{s: domainclientversion.DefaultSettings()}
	m, err := NewManager(repo, allowGate{})
	require.NoError(t, err)

	require.NoError(t, m.UpdateSettings(context.Background(), domainclientversion.UpdateCommand{
		ActorID: "admin-1",
		Settings: domainclientversion.Settings{
			Latest: "  1.4.0  ", MinimumSupported: "1.2.0", DownloadURL: " https://x ", Mandatory: true,
		},
	}))

	latest, minimum, url, _, mandatory := m.ClientVersion()
	assert.Equal(t, "1.4.0", latest) // trim
	assert.Equal(t, "1.2.0", minimum)
	assert.Equal(t, "https://x", url)
	assert.True(t, mandatory)
	assert.Equal(t, "admin-1", repo.s.UpdatedBy)
	assert.Positive(t, repo.s.UpdatedAt)
}

func TestManager_GateDenies(t *testing.T) {
	repo := &fakeRepo{s: domainclientversion.DefaultSettings()}
	m, err := NewManager(repo, denyGate{})
	require.NoError(t, err)

	_, err = m.GetSettings(context.Background(), "u1")
	assert.ErrorIs(t, err, errForbidden)
	err = m.UpdateSettings(context.Background(), domainclientversion.UpdateCommand{ActorID: "u1", Settings: domainclientversion.Settings{Latest: "9"}})
	assert.ErrorIs(t, err, errForbidden)
	assert.NotEqual(t, "9", repo.s.Latest) // 저장 안 됨
}
