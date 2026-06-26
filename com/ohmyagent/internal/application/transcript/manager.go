// Package transcriptapp 는 대화 이력 저장 설정/백엔드 스위칭 유스케이스를 담는다.
// 활성 Store 를 atomic 으로 보관하여 어드민이 런타임에 백엔드(DB/파일/S3)를 교체할 수 있게 한다.
package transcriptapp

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// accessGate 는 admin 인가 게이트다(auth 도메인 직접 import 회피).
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// storeFactory 는 설정으로 Store 를 만들고 연결을 검증하는 능력이다.
type storeFactory interface {
	Build(s domaintranscript.Settings) (domaintranscript.Store, error)
	Ping(ctx context.Context, s domaintranscript.Settings) error
}

// 컴파일 타임 인터페이스 만족 검증(recorder 가 Manager 를 Store 로 사용).
var _ domaintranscript.Store = (*Manager)(nil)

// Manager 는 활성 저장 백엔드와 설정을 관리한다.
type Manager struct {
	repo             domaintranscript.SettingsRepository
	factory          storeFactory
	cipher           domaintranscript.Cipher
	gate             accessGate
	store            atomic.Pointer[domaintranscript.Store]
	enabled          atomic.Bool
	stripAttachments atomic.Bool
}

// NewManager 는 설정을 로드해 활성 Store 를 구성한다.
func NewManager(repo domaintranscript.SettingsRepository, factory storeFactory, cipher domaintranscript.Cipher, gate accessGate) (*Manager, error) {
	m := &Manager{repo: repo, factory: factory, cipher: cipher, gate: gate}
	if err := m.reload(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// reload 는 저장된 설정으로 활성 Store 를 재구성한다. 백엔드 생성 실패 시 DB 기본값으로 폴백한다.
func (m *Manager) reload(ctx context.Context) error {
	s, err := m.repo.Get(ctx)
	if err != nil {
		return err
	}
	st, err := m.factory.Build(s)
	if err != nil {
		slog.Warn("transcript store build failed; falling back to db", "event", "transcript.settings", "backend", string(s.Backend), "error", err)
		if st, err = m.factory.Build(domaintranscript.DefaultSettings()); err != nil {
			return err
		}
	}
	m.store.Store(&st)
	m.enabled.Store(s.Enabled)
	m.stripAttachments.Store(s.StripAttachments)
	return nil
}

// StripAttachments 는 agent 첨부 본문 제거 옵션 활성 여부를 반환한다(핸들러가 기록 전 확인).
func (m *Manager) StripAttachments() bool { return m.stripAttachments.Load() }

// RunPurge 는 보존 정책(TTL)에 따라 주기적으로 오래된 이력을 삭제한다(기동 직후 1회 + interval).
// ctx 종료 시 반환한다. DB/파일 백엔드만 대상(S3 는 라이프사이클).
func (m *Manager) RunPurge(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if n, err := m.purgeOnce(ctx); err != nil {
			slog.Warn("transcript purge failed", "event", "transcript.purge", "error", err)
		} else if n > 0 {
			slog.Info("transcript purge done", "event", "transcript.purge", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// purgeOnce 는 현재 설정의 보존 일수로 활성 Store(Purger 지원 시)에서 만료 이력을 1회 삭제한다.
func (m *Manager) purgeOnce(ctx context.Context) (int, error) {
	s, err := m.repo.Get(ctx)
	if err != nil {
		return 0, err
	}
	if s.RetentionDays <= 0 {
		return 0, nil // 무기한 보존
	}
	sp := m.store.Load()
	if sp == nil {
		return 0, nil
	}
	if p, ok := (*sp).(domaintranscript.Purger); ok {
		cutoff := time.Now().Add(-time.Duration(s.RetentionDays) * 24 * time.Hour)
		return p.Purge(ctx, cutoff)
	}
	return 0, nil // S3 등 purge 미지원
}

// Save 는 domaintranscript.Store 구현으로, 현재 활성 백엔드에 위임한다(recorder 가 호출).
func (m *Manager) Save(ctx context.Context, t domaintranscript.Transcript) error {
	sp := m.store.Load()
	if sp == nil {
		return nil
	}
	return (*sp).Save(ctx, t)
}

// Enabled 는 대화 이력 저장 활성 여부를 반환한다(recorder 가 enqueue 전 확인).
func (m *Manager) Enabled() bool { return m.enabled.Load() }

// GetSettings 는 어드민 표시용 설정을 반환한다(S3 시크릿은 마스킹, 존재 여부만 hasSecret).
func (m *Manager) GetSettings(ctx context.Context, actorID string) (s domaintranscript.Settings, hasSecret bool, err error) {
	if err = m.gate.RequireAdmin(ctx, actorID); err != nil {
		return domaintranscript.Settings{}, false, err
	}
	s, err = m.repo.Get(ctx)
	if err != nil {
		return domaintranscript.Settings{}, false, err
	}
	hasSecret = s.S3SecretKey != ""
	s.S3SecretKey = "" // 평문/암호문 모두 노출 금지
	return s, hasSecret, nil
}

// UpdateSettings 는 설정을 검증·저장하고 활성 Store 를 재구성한다(admin↑).
// S3 시크릿이 비면 기존 값 보존, 입력되면 AES-GCM 암호화. 저장 전 백엔드 생성 가능 여부 확인.
func (m *Manager) UpdateSettings(ctx context.Context, cmd domaintranscript.UpdateCommand) error {
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
		s.S3SecretKey = existing.S3SecretKey // 기존 암호문 보존
	} else {
		enc, err := m.cipher.Encrypt(s.S3SecretKey)
		if err != nil {
			return &domaintranscript.ErrValidation{Msg: "S3 시크릿 암호화 실패: APP_ENCRYPTION_SECRET 설정이 필요합니다"}
		}
		s.S3SecretKey = enc
	}
	if _, err := m.factory.Build(s); err != nil {
		return &domaintranscript.ErrValidation{Msg: "저장 백엔드 생성 실패: " + err.Error()}
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
