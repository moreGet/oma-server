package transcriptout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// 컴파일 타임 인터페이스 만족 검증.
var (
	_ domaintranscript.Store  = (*fileStore)(nil)
	_ domaintranscript.Purger = (*fileStore)(nil)
)

// fileStore 는 대화 이력을 로컬 디렉터리에 `<dir>/<YYYY-MM-DD>/<id>.json.gz` 로 저장한다.
type fileStore struct {
	dir string
}

// newFileStore 는 디렉터리를 보장하고 fileStore 를 생성한다.
func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("transcript file: mkdir %q: %w", dir, err)
	}
	return &fileStore{dir: dir}, nil
}

func (s *fileStore) Save(_ context.Context, t domaintranscript.Transcript) error {
	blob, err := marshalGzip(t)
	if err != nil {
		return err
	}
	day := t.CreatedAt.UTC().Format("2006-01-02")
	dir := filepath.Join(s.dir, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("transcript file: mkdir %q: %w", dir, err)
	}
	path := filepath.Join(dir, t.ID+".json.gz")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return fmt.Errorf("transcript file: write %q: %w", path, err)
	}
	return nil
}

// Purge 는 ModTime 이 olderThan 이전인 파일을 삭제하고(빈 날짜 디렉터리도 정리) 삭제 건수를 반환한다.
func (s *fileStore) Purge(_ context.Context, olderThan time.Time) (int, error) {
	dayDirs, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("transcript file: readdir %q: %w", s.dir, err)
	}
	deleted := 0
	for _, d := range dayDirs {
		if !d.IsDir() {
			continue
		}
		dirPath := filepath.Join(s.dir, d.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		remaining := 0
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				remaining++
				continue
			}
			if info.ModTime().Before(olderThan) {
				if os.Remove(filepath.Join(dirPath, f.Name())) == nil {
					deleted++
					continue
				}
			}
			remaining++
		}
		if remaining == 0 {
			_ = os.Remove(dirPath) // 빈 날짜 디렉터리 제거
		}
	}
	return deleted, nil
}
