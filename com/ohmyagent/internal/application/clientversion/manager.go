// Package clientversionapp 는 클라이언트 버전 설정 관리/조회 유스케이스를 담는다.
// 어드민이 편집(DB 저장)하고, API 핸들러는 atomic 캐시 스냅샷을 무락으로 읽는다.
package clientversionapp

import (
	"context"
	"sync/atomic"
	"time"

	domainclientversion "aiagent/com/ohmyagent/internal/domain/clientversion"
)

// accessGate 는 admin 인가 게이트다.
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// Manager 는 클라이언트 버전 설정 서비스다(atomic 캐시).
type Manager struct {
	repo domainclientversion.SettingsRepository
	gate accessGate
	now  func() time.Time
	cur  atomic.Pointer[domainclientversion.Settings]
}

// NewManager 는 Manager 를 생성하고 현재 설정을 캐시에 적재한다.
func NewManager(repo domainclientversion.SettingsRepository, gate accessGate) (*Manager, error) {
	m := &Manager{repo: repo, gate: gate, now: time.Now}
	if err := m.reload(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) reload(ctx context.Context) error {
	s, err := m.repo.Get(ctx)
	if err != nil {
		return err
	}
	s.Normalize()
	m.cur.Store(&s)
	return nil
}

func (m *Manager) current() domainclientversion.Settings {
	if p := m.cur.Load(); p != nil {
		return *p
	}
	return domainclientversion.DefaultSettings()
}

// GetSettings 는 어드민 표시용 현재 설정을 반환한다(admin 게이트).
func (m *Manager) GetSettings(ctx context.Context, actorID string) (domainclientversion.Settings, error) {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domainclientversion.Settings{}, err
	}
	return m.current(), nil
}

// UpdateSettings 는 설정을 정규화·저장하고 캐시를 즉시 갱신한다(admin 게이트).
func (m *Manager) UpdateSettings(ctx context.Context, cmd domainclientversion.UpdateCommand) error {
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

// --- VersionProvider (API 핸들러용 무락 조회) ---

// ClientVersion 은 현재 버전 정보를 반환한다(API 핸들러가 사용).
func (m *Manager) ClientVersion() (latest, minimumSupported, downloadURL, notice string, mandatory bool) {
	s := m.current()
	return s.Latest, s.MinimumSupported, s.DownloadURL, s.Notice, s.Mandatory
}
