// Package toolpolicyapp 는 서버 제어형 도구 정책의 관리/조회 유스케이스를 담는다.
// 어드민이 편집(DB 저장)하고, API 핸들러는 atomic 캐시 스냅샷을 무락(lock-free)으로 읽는다.
// 정책은 전역(단일 행) + 멤버별 오버라이드로 구성되며, 유효 정책은 계층 병합으로 산출한다.
package toolpolicyapp

import (
	"context"
	"sync/atomic"
	"time"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// accessGate 는 admin 인가 게이트다(auth 도메인 직접 import 회피).
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// Manager 는 도구 정책 서비스다. 전역 정책과 멤버 오버라이드를 atomic 캐시에 보관해 핫패스 조회를 빠르게 한다.
type Manager struct {
	repo       domaintoolpolicy.SettingsRepository
	memberRepo domaintoolpolicy.MemberPolicyRepository
	gate       accessGate
	now        func() time.Time
	cur        atomic.Pointer[domaintoolpolicy.Settings]
	members    atomic.Pointer[map[string]domaintoolpolicy.MemberPolicy]
}

// NewManager 는 Manager 를 생성하고 전역 정책 + 멤버 오버라이드를 캐시에 적재한다.
func NewManager(repo domaintoolpolicy.SettingsRepository, memberRepo domaintoolpolicy.MemberPolicyRepository, gate accessGate) (*Manager, error) {
	m := &Manager{repo: repo, memberRepo: memberRepo, gate: gate, now: time.Now}
	if err := m.reload(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// reload 는 DB 에서 전역 정책 + 멤버 오버라이드를 읽어 정규화 후 캐시에 저장한다.
func (m *Manager) reload(ctx context.Context) error {
	s, err := m.repo.Get(ctx)
	if err != nil {
		return err
	}
	s.Normalize()
	m.cur.Store(&s)

	list, err := m.memberRepo.All(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]domaintoolpolicy.MemberPolicy, len(list))
	for _, p := range list {
		p.Normalize()
		if p.IsEmpty() {
			continue
		}
		byID[p.MemberID] = p
	}
	m.members.Store(&byID)
	return nil
}

// current 는 캐시된 전역 정책을 반환한다(없으면 기본값).
func (m *Manager) current() domaintoolpolicy.Settings {
	if p := m.cur.Load(); p != nil {
		return *p
	}
	return domaintoolpolicy.DefaultSettings()
}

// memberPolicy 는 캐시된 멤버 오버라이드를 반환한다(없으면 ok=false).
func (m *Manager) memberPolicy(memberID string) (domaintoolpolicy.MemberPolicy, bool) {
	if mp := m.members.Load(); mp != nil {
		p, ok := (*mp)[memberID]
		return p, ok
	}
	return domaintoolpolicy.MemberPolicy{}, false
}

// --- 어드민(전역) ---

// GetSettings 는 어드민 표시용 현재 전역 정책을 반환한다(admin 게이트).
func (m *Manager) GetSettings(ctx context.Context, actorID string) (domaintoolpolicy.Settings, error) {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domaintoolpolicy.Settings{}, err
	}
	return m.current(), nil
}

// UpdateSettings 는 전역 정책을 정규화·저장하고 캐시를 즉시 갱신한다(admin 게이트).
func (m *Manager) UpdateSettings(ctx context.Context, cmd domaintoolpolicy.UpdateCommand) error {
	if err := m.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	s := cmd.Settings
	s.Normalize()
	s.UpdatedBy = cmd.ActorID
	s.UpdatedAt = m.now().UTC().Unix()
	if err := m.repo.Save(ctx, s); err != nil {
		return err
	}
	return m.reload(ctx)
}

// --- 어드민(멤버 오버라이드) ---

// GetMemberPolicy 는 어드민 표시용 멤버 오버라이드를 반환한다(admin 게이트).
func (m *Manager) GetMemberPolicy(ctx context.Context, actorID, memberID string) (domaintoolpolicy.MemberPolicy, error) {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domaintoolpolicy.MemberPolicy{}, err
	}
	return m.memberRepo.Get(ctx, memberID)
}

// UpdateMemberPolicy 는 멤버 오버라이드를 정규화·저장하고 캐시를 즉시 갱신한다(admin 게이트).
// 오버라이드가 비면 행을 삭제해 전역만 적용되게 한다.
func (m *Manager) UpdateMemberPolicy(ctx context.Context, cmd domaintoolpolicy.MemberUpdateCommand) error {
	if err := m.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	p := domaintoolpolicy.MemberPolicy{MemberID: cmd.MemberID, Enabled: cmd.Enabled, Disabled: cmd.Disabled}
	p.Normalize()
	if p.IsEmpty() {
		if err := m.memberRepo.Delete(ctx, cmd.MemberID); err != nil {
			return err
		}
		return m.reload(ctx)
	}
	p.UpdatedBy = cmd.ActorID
	p.UpdatedAt = m.now().UTC().Unix()
	if err := m.memberRepo.Save(ctx, p); err != nil {
		return err
	}
	return m.reload(ctx)
}

// MemberPolicies 는 현재 멤버 오버라이드 스냅샷(복사본)을 반환한다(어드민 목록 표시용, 무락).
func (m *Manager) MemberPolicies() map[string]domaintoolpolicy.MemberPolicy {
	out := make(map[string]domaintoolpolicy.MemberPolicy)
	if mp := m.members.Load(); mp != nil {
		for k, v := range *mp {
			out[k] = v
		}
	}
	return out
}

// --- PolicyProvider (API 핸들러용 무락 조회, 게이트 없음) ---

// EffectivePolicy 는 멤버에 적용되는 유효 도구 정책(전역 ⊕ 멤버 오버라이드)을 반환한다.
func (m *Manager) EffectivePolicy(memberID string) (mode string, enabled, disabled []string) {
	g := m.current()
	if p, ok := m.memberPolicy(memberID); ok {
		return domaintoolpolicy.ResolveEffective(g, &p)
	}
	return domaintoolpolicy.ResolveEffective(g, nil)
}

// CommandPolicy 는 현재 위험명령/경로 차단 패턴을 반환한다(전역 전용).
func (m *Manager) CommandPolicy() (patterns []domaintoolpolicy.BlockedPattern, paths []domaintoolpolicy.BlockedPath) {
	s := m.current()
	return s.BlockedPatterns, s.BlockedPaths
}
