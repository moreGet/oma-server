package project

import (
	"context"
	"strings"
)

// Backend 은 대화 본문 저장 백엔드 종류다.
type Backend string

const (
	BackendDB   Backend = "db"
	BackendFile Backend = "file"
	BackendS3   Backend = "s3"
)

// Settings 는 세션(대화) 저장 설정이다(저장 백엔드 + 계정별 전역 최대 세션 수).
type Settings struct {
	Backend            Backend
	FileDir            string
	S3Endpoint         string
	S3Bucket           string
	S3Region           string
	S3AccessKey        string
	S3SecretKey        string // 저장 시 암호문, 입력 시 평문
	S3UseSSL           bool
	DefaultMaxSessions int // 계정별 전역 최대 세션 수(0 = 무제한)
	UpdatedAt          int64
	UpdatedBy          string
}

// DefaultSettings 는 기본값(DB 백엔드, 무제한)이다.
func DefaultSettings() Settings {
	return Settings{Backend: BackendDB, S3UseSSL: true, DefaultMaxSessions: 0}
}

func (s *Settings) Normalize() {
	if s.Backend == "" {
		s.Backend = BackendDB
	}
	s.FileDir = strings.TrimSpace(s.FileDir)
	s.S3Endpoint = strings.TrimSpace(s.S3Endpoint)
	s.S3Bucket = strings.TrimSpace(s.S3Bucket)
	s.S3Region = strings.TrimSpace(s.S3Region)
	s.S3AccessKey = strings.TrimSpace(s.S3AccessKey)
	if s.DefaultMaxSessions < 0 {
		s.DefaultMaxSessions = 0
	}
}

func (s Settings) Validate() error {
	switch s.Backend {
	case BackendDB:
	case BackendFile:
		if s.FileDir == "" {
			return &ErrValidation{Msg: "파일 백엔드는 저장 디렉터리가 필요합니다"}
		}
	case BackendS3:
		if s.S3Endpoint == "" || s.S3Bucket == "" || s.S3AccessKey == "" {
			return &ErrValidation{Msg: "S3 백엔드는 endpoint·bucket·access_key 가 필요합니다"}
		}
	default:
		return &ErrValidation{Msg: "알 수 없는 백엔드입니다"}
	}
	return nil
}

// UpdateSettingsCommand 는 세션 저장 설정 변경 입력이다.
type UpdateSettingsCommand struct {
	Settings Settings
	ActorID  string
}

// SettingsRepository — 세션 저장 설정 영속화(단일 행).
type SettingsRepository interface {
	Get(ctx context.Context) (Settings, error)
	Save(ctx context.Context, s Settings) error
}

// MemberLimitRepository — 멤버별 최대 세션 수 오버라이드(0 = 전역 기본값).
type MemberLimitRepository interface {
	Get(ctx context.Context, memberID string) (int, error)
	Set(ctx context.Context, memberID string, max int) error
	All(ctx context.Context) (map[string]int, error)
}
