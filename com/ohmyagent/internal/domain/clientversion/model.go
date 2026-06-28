// Package clientversion 은 클라이언트 버전 점검(GET /api/v1/client/version) 설정의 도메인 모델·포트를 담는다.
// 값은 DB(단일 행)에 보관하고 어드민이 편집한다.
package clientversion

import (
	"context"
	"strings"
)

// Settings 는 클라이언트 버전 점검 설정이다.
type Settings struct {
	Latest           string
	MinimumSupported string
	DownloadURL      string
	Notice           string
	Mandatory        bool
	UpdatedAt        int64
	UpdatedBy        string
}

// DefaultSettings 는 기본 설정을 반환한다.
func DefaultSettings() Settings {
	return Settings{Latest: "1.0.0", MinimumSupported: "1.0.0"}
}

// Normalize 는 공백을 정리한다(저장 전).
func (s *Settings) Normalize() {
	s.Latest = strings.TrimSpace(s.Latest)
	s.MinimumSupported = strings.TrimSpace(s.MinimumSupported)
	s.DownloadURL = strings.TrimSpace(s.DownloadURL)
	s.Notice = strings.TrimSpace(s.Notice)
}

// UpdateCommand 는 어드민의 버전 설정 갱신 명령이다.
type UpdateCommand struct {
	Settings Settings
	ActorID  string
}

// SettingsRepository — out 포트(단일 행 영속화).
type SettingsRepository interface {
	Get(ctx context.Context) (Settings, error)
	Save(ctx context.Context, s Settings) error
}
