// Package toolpolicyapp 는 서버 제어형 도구 정책의 관리/조회 유스케이스를 담는다.
// 어드민이 편집(DB 저장)하고, API 핸들러는 atomic 캐시 스냅샷을 무락(lock-free)으로 읽는다.
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

// Manager 는 도구 정책 서비스다. 현재 정책을 atomic 캐시에 보관해 핫패스 조회를 빠르게 한다.
type Manager struct {
	repo domaintoolpolicy.SettingsRepository
	gate accessGate
	now  func() time.Time
	cur  atomic.Pointer[domaintoolpolicy.Settings]
}

// NewManager 는 Manager 를 생성하고 현재 정책을 캐시에 적재한다.
func NewManager(repo domaintoolpolicy.SettingsRepository, gate accessGate) (*Manager, error) {
	m := &Manager{repo: repo, gate: gate, now: time.Now}
	if err := m.reload(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// reload 는 DB 에서 정책을 읽어 정규화 후 캐시에 저장한다.
func (m *Manager) reload(ctx context.Context) error {
	s, err := m.repo.Get(ctx)
	if err != nil {
		return err
	}
	s.Normalize()
	m.cur.Store(&s)
	return nil
}

// current 는 캐시된 현재 정책을 반환한다(없으면 기본값).
func (m *Manager) current() domaintoolpolicy.Settings {
	if p := m.cur.Load(); p != nil {
		return *p
	}
	return domaintoolpolicy.DefaultSettings()
}

// GetSettings 는 어드민 표시용 현재 정책을 반환한다(admin 게이트).
func (m *Manager) GetSettings(ctx context.Context, actorID string) (domaintoolpolicy.Settings, error) {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domaintoolpolicy.Settings{}, err
	}
	return m.current(), nil
}

// UpdateSettings 는 정책을 정규화·저장하고 캐시를 즉시 갱신한다(admin 게이트).
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

// --- PolicyProvider (API 핸들러용 무락 조회, 게이트 없음) ---

// ToolPolicy 는 현재 도구 실행/노출 정책(모드 + enabled/disabled)을 반환한다.
func (m *Manager) ToolPolicy() (mode string, enabled, disabled []string) {
	s := m.current()
	return s.Mode, s.Enabled, s.Disabled
}

// CommandPolicy 는 현재 위험명령/경로 차단 패턴을 반환한다.
func (m *Manager) CommandPolicy() (patterns []domaintoolpolicy.BlockedPattern, paths []domaintoolpolicy.BlockedPath) {
	s := m.current()
	return s.BlockedPatterns, s.BlockedPaths
}
