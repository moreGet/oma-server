package transcriptout

import (
	"context"
	"fmt"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintranscript.StoreFactory = (*StoreFactory)(nil)

// StoreFactory 는 Settings 로부터 활성 Store 백엔드를 생성한다.
// DB store 는 미리 만들어 주입받고(파일/S3 만 여기서 생성), S3 시크릿은 cipher 로 복호화한다.
type StoreFactory struct {
	dbStore domaintranscript.Store
	cipher  domaintranscript.Cipher
}

// NewStoreFactory 는 StoreFactory 를 생성한다.
func NewStoreFactory(dbStore domaintranscript.Store, cipher domaintranscript.Cipher) *StoreFactory {
	return &StoreFactory{dbStore: dbStore, cipher: cipher}
}

// Build 는 설정의 backend 에 맞는 Store 를 만든다.
func (f *StoreFactory) Build(s domaintranscript.Settings) (domaintranscript.Store, error) {
	switch s.Backend {
	case domaintranscript.BackendDB, "":
		return f.dbStore, nil
	case domaintranscript.BackendFile:
		return newFileStore(s.FileDir)
	case domaintranscript.BackendS3:
		secret, err := f.decryptSecret(s.S3SecretKey)
		if err != nil {
			return nil, fmt.Errorf("transcript: decrypt s3 secret: %w", err)
		}
		return newS3Store(s.S3Endpoint, s.S3Bucket, s.S3Region, s.S3AccessKey, secret, s.S3UseSSL)
	default:
		return nil, fmt.Errorf("transcript: unknown backend %q", s.Backend)
	}
}

// Ping 은 설정 백엔드의 연결을 검증한다(어드민 연결 테스트). db/file 은 항상 성공, s3 는 버킷 확인.
func (f *StoreFactory) Ping(ctx context.Context, s domaintranscript.Settings) error {
	st, err := f.Build(s)
	if err != nil {
		return err
	}
	if p, ok := st.(interface{ ping(context.Context) error }); ok {
		return p.ping(ctx)
	}
	return nil
}

func (f *StoreFactory) decryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	return f.cipher.Decrypt(enc)
}
