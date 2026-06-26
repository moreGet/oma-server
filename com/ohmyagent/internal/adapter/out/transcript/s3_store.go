package transcriptout

import (
	"bytes"
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintranscript.Store = (*s3Store)(nil)

// s3Store 는 대화 이력을 S3 호환 스토리지에 `<YYYY/MM/DD>/<source>/<id>.json.gz` 로 PUT 한다.
type s3Store struct {
	client *minio.Client
	bucket string
}

// newS3Store 는 minio 클라이언트를 만들어 s3Store 를 생성한다.
func newS3Store(endpoint, bucket, region, accessKey, secretKey string, useSSL bool) (*s3Store, error) {
	cl, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("transcript s3: client: %w", err)
	}
	return &s3Store{client: cl, bucket: bucket}, nil
}

func (s *s3Store) Save(ctx context.Context, t domaintranscript.Transcript) error {
	blob, err := marshalGzip(t)
	if err != nil {
		return err
	}
	day := t.CreatedAt.UTC().Format("2006/01/02")
	key := fmt.Sprintf("%s/%s/%s.json.gz", day, t.Source, t.ID)
	_, err = s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(blob), int64(len(blob)),
		minio.PutObjectOptions{ContentType: "application/json", ContentEncoding: "gzip"})
	if err != nil {
		return fmt.Errorf("transcript s3: put %s: %w", key, err)
	}
	return nil
}

// ping 은 버킷 존재 여부로 연결을 검증한다(어드민 연결 테스트용).
func (s *s3Store) ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("transcript s3: bucket check: %w", err)
	}
	if !ok {
		return fmt.Errorf("transcript s3: bucket %q not found or inaccessible", s.bucket)
	}
	return nil
}
