package transcript

import (
	"context"
	"strings"
)

// ErrValidation 은 설정 검증 실패다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// Backend 는 대화 이력 저장 백엔드 종류다.
type Backend string

const (
	BackendDB   Backend = "db"   // DB gzip BLOB(기본)
	BackendFile Backend = "file" // 로컬 디렉터리 gzip 파일
	BackendS3   Backend = "s3"   // S3 호환 오브젝트 스토리지(minio-go)
)

// Settings 는 대화 이력 저장 설정이다(어드민에서 변경).
// S3SecretKey 는 DB 에 AES-GCM 암호문으로 저장되며 응답에는 노출하지 않는다.
type Settings struct {
	Enabled     bool
	Backend     Backend
	FileDir     string
	S3Endpoint  string // 예: s3.amazonaws.com, minio:9000
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string // 저장 시 암호문, 입력 시 평문
	S3UseSSL    bool

	RetentionDays    int  // 보존 일수. 0 = 무기한(purge 안 함). DB/파일 백엔드에 적용
	StripAttachments bool // true 면 agent 첨부 본문(base64)을 이력에서 제거(메타데이터만 보존)

	UpdatedAt int64
	UpdatedBy string
}

// UpdateCommand 는 설정 변경 입력이다.
type UpdateCommand struct {
	Settings Settings
	ActorID  string
}

// DefaultSettings 는 기본 설정(DB 백엔드, 활성)이다.
func DefaultSettings() Settings {
	return Settings{Enabled: true, Backend: BackendDB, S3UseSSL: true}
}

// Normalize 는 입력 공백/대소문자를 정규화한다.
func (s *Settings) Normalize() {
	s.Backend = Backend(strings.ToLower(strings.TrimSpace(string(s.Backend))))
	if s.Backend == "" {
		s.Backend = BackendDB
	}
	s.FileDir = strings.TrimSpace(s.FileDir)
	s.S3Endpoint = strings.TrimSpace(s.S3Endpoint)
	s.S3Bucket = strings.TrimSpace(s.S3Bucket)
	s.S3Region = strings.TrimSpace(s.S3Region)
	s.S3AccessKey = strings.TrimSpace(s.S3AccessKey)
	s.S3SecretKey = strings.TrimSpace(s.S3SecretKey)
}

// Validate 는 백엔드별 필수 필드를 검증한다.
func (s Settings) Validate() error {
	if s.RetentionDays < 0 {
		return &ErrValidation{Msg: "retention_days 는 0 이상이어야 합니다(0=무기한)"}
	}
	switch s.Backend {
	case BackendDB:
	case BackendFile:
		if s.FileDir == "" {
			return &ErrValidation{Msg: "file 백엔드는 저장 디렉터리(file_dir)가 필요합니다"}
		}
	case BackendS3:
		if s.S3Endpoint == "" || s.S3Bucket == "" || s.S3AccessKey == "" {
			return &ErrValidation{Msg: "s3 백엔드는 endpoint·bucket·access_key 가 필요합니다"}
		}
	default:
		return &ErrValidation{Msg: "backend 는 db|file|s3 중 하나여야 합니다"}
	}
	return nil
}

// SettingsRepository — out 포트(단일 행 설정 영속화).
type SettingsRepository interface {
	Get(ctx context.Context) (Settings, error)  // 없으면 DefaultSettings
	Save(ctx context.Context, s Settings) error // upsert(id=1)
}

// Cipher — out 포트(S3 시크릿 암복호화). AESGCMCipher 가 충족한다.
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// StoreFactory — out 포트(Settings → 활성 Store 생성).
type StoreFactory interface {
	Build(s Settings) (Store, error)
}
