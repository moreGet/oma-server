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

// fakeMemberRepo 는 멤버 오버라이드 인메모리 레포다.
type fakeMemberRepo struct {
	m map[string]domaintoolpolicy.MemberPolicy
}

func newFakeMemberRepo() *fakeMemberRepo {
	return &fakeMemberRepo{m: map[string]domaintoolpolicy.MemberPolicy{}}
}

func (r *fakeMemberRepo) Get(_ context.Context, id string) (domaintoolpolicy.MemberPolicy, error) {
	if p, ok := r.m[id]; ok {
		return p, nil
	}
	return domaintoolpolicy.MemberPolicy{MemberID: id}, nil
}

func (r *fakeMemberRepo) All(context.Context) ([]domaintoolpolicy.MemberPolicy, error) {
	out := make([]domaintoolpolicy.MemberPolicy, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	return out, nil
}

func (r *fakeMemberRepo) Save(_ context.Context, p domaintoolpolicy.MemberPolicy) error {
	r.m[p.MemberID] = p
	return nil
}

func (r *fakeMemberRepo) Delete(_ context.Context, id string) error {
	delete(r.m, id)
	return nil
}

type allowGate struct{}

func (allowGate) RequireAdmin(context.Context, string) error { return nil }

type denyGate struct{}

var errForbidden = errors.New("forbidden")

func (denyGate) RequireAdmin(context.Context, string) error { return errForbidden }

func TestManager_UpdateNormalizesReloadsAndStamps(t *testing.T) {
	repo := &fakeRepo{s: domaintoolpolicy.DefaultSettings()}
	m, err := NewManager(repo, newFakeMemberRepo(), allowGate{})
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

	// 캐시(스냅샷) 반영 확인 — 오버라이드 없는 멤버는 전역 유효 정책을 그대로 받는다.
	mode, enabled, disabled := m.EffectivePolicy("no-override-user")
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
	m, err := NewManager(repo, newFakeMemberRepo(), denyGate{})
	require.NoError(t, err)

	_, err = m.GetSettings(context.Background(), "u1")
	assert.ErrorIs(t, err, errForbidden)

	err = m.UpdateSettings(context.Background(), domaintoolpolicy.UpdateCommand{
		ActorID:  "u1",
		Settings: domaintoolpolicy.Settings{Disabled: []string{"x"}},
	})
	assert.ErrorIs(t, err, errForbidden)
	assert.Empty(t, repo.s.Disabled) // 게이트 거부 시 저장 안 됨

	err = m.UpdateMemberPolicy(context.Background(), domaintoolpolicy.MemberUpdateCommand{
		MemberID: "m1", ActorID: "u1", Disabled: []string{"x"},
	})
	assert.ErrorIs(t, err, errForbidden)
}

func TestManager_MemberPolicyLayeredMerge(t *testing.T) {
	repo := &fakeRepo{s: domaintoolpolicy.DefaultSettings()}
	m, err := NewManager(repo, newFakeMemberRepo(), allowGate{})
	require.NoError(t, err)

	// 전역: kill_process 차단(나머지 전체 허용).
	require.NoError(t, m.UpdateSettings(context.Background(), domaintoolpolicy.UpdateCommand{
		ActorID:  "admin",
		Settings: domaintoolpolicy.Settings{Disabled: []string{"kill_process"}},
	}))
	// 멤버 u1: screenshot 추가 차단(계층 병합 = 전역 ∪ 멤버).
	require.NoError(t, m.UpdateMemberPolicy(context.Background(), domaintoolpolicy.MemberUpdateCommand{
		MemberID: "u1", ActorID: "admin", Disabled: []string{"screenshot"},
	}))

	_, _, disabled := m.EffectivePolicy("u1")
	assert.ElementsMatch(t, []string{"kill_process", "screenshot"}, disabled)

	// 오버라이드 없는 멤버는 전역만 적용.
	_, _, d2 := m.EffectivePolicy("u2")
	assert.Equal(t, []string{"kill_process"}, d2)

	// 빈 오버라이드 저장 → 행 삭제(전역만 적용).
	require.NoError(t, m.UpdateMemberPolicy(context.Background(), domaintoolpolicy.MemberUpdateCommand{
		MemberID: "u1", ActorID: "admin",
	}))
	_, _, d3 := m.EffectivePolicy("u1")
	assert.Equal(t, []string{"kill_process"}, d3)
	assert.Empty(t, m.MemberPolicies())
}
