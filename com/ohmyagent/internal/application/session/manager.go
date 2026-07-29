// Package sessionapp 는 대화 본문 저장 백엔드(DB/파일/S3) 스위칭 + 계정별 최대 세션 수 설정을 관리한다.
// 활성 ContentStore 를 atomic 으로 보관해 어드민이 런타임에 백엔드를 교체할 수 있게 한다.
package sessionapp

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

type storeFactory interface {
	Build(s domainproject.Settings) (domainproject.ContentStore, error)
	Ping(ctx context.Context, s domainproject.Settings) error
}

var _ domainproject.ContentStore = (*Manager)(nil)

// Manager 는 활성 저장 백엔드 + 세션 캡 설정을 관리한다.
type Manager struct {
	repo       domainproject.SettingsRepository
	limits     domainproject.MemberLimitRepository
	factory    storeFactory
	cipher     domainproject.Cipher
	gate       accessGate
	store      atomic.Pointer[domainproject.ContentStore]
	defaultMax atomic.Int64
}

func NewManager(repo domainproject.SettingsRepository, limits domainproject.MemberLimitRepository, factory storeFactory, cipher domainproject.Cipher, gate accessGate) (*Manager, error) {
	m := &Manager{repo: repo, limits: limits, factory: factory, cipher: cipher, gate: gate}
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
	st, err := m.factory.Build(s)
	if err != nil {
		slog.Warn("session store build failed; falling back to db", "event", "session.settings", "backend", string(s.Backend), "error", err)
		if st, err = m.factory.Build(domainproject.DefaultSettings()); err != nil {
			return err
		}
	}
	m.store.Store(&st)
	m.defaultMax.Store(int64(s.DefaultMaxSessions))
	return nil
}

// Save 는 domainproject.ContentStore 구현으로, 활성 백엔드에 위임한다.
func (m *Manager) Save(ctx context.Context, key string, data []byte) error {
	sp := m.store.Load()
	if sp == nil {
		return nil
	}
	return (*sp).Save(ctx, key, data)
}

// EffectiveMaxSessions 는 멤버 오버라이드(>0) 우선, 없으면 전역 기본값을 반환한다(0 = 무제한).
func (m *Manager) EffectiveMaxSessions(ctx context.Context, memberID string) (int, error) {
	ml, err := m.limits.Get(ctx, memberID)
	if err != nil {
		return 0, err
	}
	if ml > 0 {
		return ml, nil
	}
	return int(m.defaultMax.Load()), nil
}

// --- 어드민 ---

// GetSettings 는 표시용 설정을 반환한다(S3 시크릿 마스킹, 존재 여부만).
func (m *Manager) GetSettings(ctx context.Context, actorID string) (s domainproject.Settings, hasSecret bool, err error) {
	if err = m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domainproject.Settings{}, false, err
	}
	s, err = m.repo.Get(ctx)
	if err != nil {
		return domainproject.Settings{}, false, err
	}
	hasSecret = s.S3SecretKey != ""
	s.S3SecretKey = ""
	return s, hasSecret, nil
}

// UpdateSettings 는 설정을 검증·저장하고 활성 백엔드를 재구성한다(admin↑).
func (m *Manager) UpdateSettings(ctx context.Context, cmd domainproject.UpdateSettingsCommand) error {
	if err := m.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	s := cmd.Settings
	s.Normalize()
	if err := s.Validate(); err != nil {
		return err
	}
	if s.S3SecretKey == "" {
		existing, err := m.repo.Get(ctx)
		if err != nil {
			return err
		}
		s.S3SecretKey = existing.S3SecretKey
	} else {
		enc, err := m.cipher.Encrypt(s.S3SecretKey)
		if err != nil {
			return &domainproject.ErrValidation{Msg: "S3 시크릿 암호화 실패: APP_ENCRYPTION_SECRET 설정이 필요합니다"}
		}
		s.S3SecretKey = enc
	}
	if _, err := m.factory.Build(s); err != nil {
		return &domainproject.ErrValidation{Msg: "저장 백엔드 생성 실패: " + err.Error()}
	}
	s.UpdatedAt = time.Now().Unix()
	s.UpdatedBy = cmd.ActorID
	if err := m.repo.Save(ctx, s); err != nil {
		return err
	}
	return m.reload(ctx)
}

// TestConnection 은 현재 저장된 설정으로 백엔드 연결을 검증한다(admin↑).
func (m *Manager) TestConnection(ctx context.Context, actorID string) error {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	s, err := m.repo.Get(ctx)
	if err != nil {
		return err
	}
	return m.factory.Ping(ctx, s)
}

// SetMemberLimit 는 멤버별 최대 세션 수를 설정한다(admin↑). 0 = 전역 기본값.
func (m *Manager) SetMemberLimit(ctx context.Context, actorID, memberID string, max int) error {
	if err := m.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	if max < 0 {
		max = 0
	}
	return m.limits.Set(ctx, memberID, max)
}

// MemberLimitsFor 는 주어진 멤버들의 세션 한도만 반환한다(어드민 목록 한 페이지분).
func (m *Manager) MemberLimitsFor(ctx context.Context, memberIDs []string) (map[string]int, error) {
	return m.limits.ByIDs(ctx, memberIDs)
}

// DefaultMaxSessions 는 전역 기본 최대 세션 수를 반환한다.
func (m *Manager) DefaultMaxSessions() int { return int(m.defaultMax.Load()) }
