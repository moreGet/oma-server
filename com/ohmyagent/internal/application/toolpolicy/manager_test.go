package toolpolicyapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

type fakeRepo struct{ s domaintoolpolicy.Settings }

func (r *fakeRepo) Get(context.Context) (domaintoolpolicy.Settings, error) { return r.s, nil }
func (r *fakeRepo) Save(_ context.Context, s domaintoolpolicy.Settings) error {
	r.s = s
	return nil
}

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

type denyGate struct{}

var errForbidden = errors.New("forbidden")

func (denyGate) RequireAdmin(context.Context, string) error { return errForbidden }

func TestManager_UpdateNormalizesReloadsAndStamps(t *testing.T) {
	repo := &fakeRepo{s: domaintoolpolicy.DefaultSettings()}
	m, err := NewManager(repo, allowGate{})
	require.NoError(t, err)

	err = m.UpdateSettings(context.Background(), domaintoolpolicy.UpdateCommand{
		ActorID: "admin-1",
		Settings: domaintoolpolicy.Settings{
			Mode:     "weird",                             // → cached 로 정규화
			Enabled:  []string{" read_file ", "", "grep"}, // trim + 빈 항목 제거
			Disabled: []string{"kill_process"},
			BlockedPatterns: []domaintoolpolicy.BlockedPattern{
				{Type: "", Pattern: "bcdedit"},  // type/script_type → substring/any
				{Type: "regex", Pattern: "   "}, // 빈 패턴 → 제거
			},
		},
	})
	require.NoError(t, err)

	// 캐시(스냅샷) 반영 확인.
	mode, enabled, disabled := m.ToolPolicy()
	assert.Equal(t, "cached", mode)
	assert.Equal(t, []string{"read_file", "grep"}, enabled)
	assert.Equal(t, []string{"kill_process"}, disabled)

	patterns, _ := m.CommandPolicy()
	require.Len(t, patterns, 1) // 빈 패턴 제거됨
	assert.Equal(t, "substring", patterns[0].Type)
	assert.Equal(t, "any", patterns[0].ScriptType)

	// 저장된 값에 actor/timestamp 가 찍힌다.
	assert.Equal(t, "admin-1", repo.s.UpdatedBy)
	assert.Positive(t, repo.s.UpdatedAt)

	// GetSettings(admin) 도 동일 스냅샷 반환.
	got, err := m.GetSettings(context.Background(), "admin-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"read_file", "grep"}, got.Enabled)
}

func TestManager_GateDenies(t *testing.T) {
	repo := &fakeRepo{s: domaintoolpolicy.DefaultSettings()}
	m, err := NewManager(repo, denyGate{})
	require.NoError(t, err)

	_, err = m.GetSettings(context.Background(), "u1")
	assert.ErrorIs(t, err, errForbidden)

	err = m.UpdateSettings(context.Background(), domaintoolpolicy.UpdateCommand{
		ActorID:  "u1",
		Settings: domaintoolpolicy.Settings{Disabled: []string{"x"}},
	})
	assert.ErrorIs(t, err, errForbidden)
	assert.Empty(t, repo.s.Disabled) // 게이트 거부 시 저장 안 됨
}
