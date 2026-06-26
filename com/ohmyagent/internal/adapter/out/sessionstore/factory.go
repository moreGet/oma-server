package sessionstore

import (
	"context"
	"fmt"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.StoreFactory = (*StoreFactory)(nil)

// StoreFactory 는 Settings 로부터 대화 본문 ContentStore 를 만든다(DB store 주입, 파일/S3 생성).
type StoreFactory struct {
	dbStore domainproject.ContentStore
	cipher  domainproject.Cipher
}

func NewStoreFactory(dbStore domainproject.ContentStore, cipher domainproject.Cipher) *StoreFactory {
	return &StoreFactory{dbStore: dbStore, cipher: cipher}
}

func (f *StoreFactory) Build(s domainproject.Settings) (domainproject.ContentStore, error) {
	switch s.Backend {
	case domainproject.BackendDB, "":
		return f.dbStore, nil
	case domainproject.BackendFile:
		return newFileStore(s.FileDir)
	case domainproject.BackendS3:
		secret, err := f.decrypt(s.S3SecretKey)
		if err != nil {
			return nil, fmt.Errorf("session: decrypt s3 secret: %w", err)
		}
		return newS3Store(s.S3Endpoint, s.S3Bucket, s.S3Region, s.S3AccessKey, secret, s.S3UseSSL)
	default:
		return nil, fmt.Errorf("session: unknown backend %q", s.Backend)
	}
}

// Ping 은 백엔드 연결을 검증한다(db/file 항상 성공, s3 버킷 확인).
func (f *StoreFactory) Ping(ctx context.Context, s domainproject.Settings) error {
	st, err := f.Build(s)
	if err != nil {
		return err
	}
	if p, ok := st.(interface{ ping(context.Context) error }); ok {
		return p.ping(ctx)
	}
	return nil
}

func (f *StoreFactory) decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	return f.cipher.Decrypt(enc)
}
