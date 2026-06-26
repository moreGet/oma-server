package sessionstore

import (
	"bytes"
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.ContentStore = (*s3Store)(nil)

type s3Store struct {
	client *minio.Client
	bucket string
}

func newS3Store(endpoint, bucket, region, accessKey, secretKey string, useSSL bool) (*s3Store, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("session s3: client: %w", err)
	}
	return &s3Store{client: client, bucket: bucket}, nil
}

func (s *s3Store) Save(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/gzip", ContentEncoding: "gzip"})
	if err != nil {
		return fmt.Errorf("session s3: put %q: %w", key, err)
	}
	return nil
}

// ping 은 버킷 존재를 확인한다(어드민 연결 테스트).
func (s *s3Store) ping(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("session s3: bucket check: %w", err)
	}
	if !exists {
		return fmt.Errorf("session s3: bucket %q not found", s.bucket)
	}
	return nil
}
