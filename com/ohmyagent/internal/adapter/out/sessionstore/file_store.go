// Package sessionstore 는 대화 본문(messages)을 파일/S3 백엔드에 저장하는 어댑터다.
// key 는 슬래시 구분 경로("owner/project/conv.json.gz")로, 디렉터리 구조로 저장된다.
package sessionstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

var _ domainproject.ContentStore = (*fileStore)(nil)

type fileStore struct{ dir string }

func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session file: mkdir %q: %w", dir, err)
	}
	return &fileStore{dir: dir}, nil
}

func (s *fileStore) Save(_ context.Context, key string, data []byte) error {
	path := filepath.Join(s.dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("session file: mkdir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("session file: write %q: %w", path, err)
	}
	return nil
}
