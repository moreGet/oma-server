package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

// TestFileStore_Save 는 슬래시 key 가 중첩 디렉터리 구조로 저장되는지 검증한다.
func TestFileStore_Save(t *testing.T) {
	dir := t.TempDir()
	fs, err := newFileStore(dir)
	require.NoError(t, err)

	require.NoError(t, fs.Save(context.Background(), "u1/p1/c1.json.gz", []byte("payload")))

	got, err := os.ReadFile(filepath.Join(dir, "u1", "p1", "c1.json.gz"))
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))

	// 같은 key 재저장(덮어쓰기).
	require.NoError(t, fs.Save(context.Background(), "u1/p1/c1.json.gz", []byte("v2")))
	got, _ = os.ReadFile(filepath.Join(dir, "u1", "p1", "c1.json.gz"))
	assert.Equal(t, "v2", string(got))
}

func TestFactory_BuildDBAndFile(t *testing.T) {
	f := NewStoreFactory(fakeContent{}, nil)
	// db → 주입된 store.
	st, err := f.Build(domainproject.Settings{Backend: domainproject.BackendDB})
	require.NoError(t, err)
	require.NotNil(t, st)
	// file → 디렉터리 생성.
	dir := t.TempDir()
	fileSettings := domainproject.Settings{Backend: domainproject.BackendFile, FileDir: dir}
	st, err = f.Build(fileSettings)
	require.NoError(t, err)
	require.NotNil(t, st)
	// Ping(db/file) 은 항상 성공.
	require.NoError(t, f.Ping(context.Background(), fileSettings))
}

type fakeContent struct{}

func (fakeContent) Save(context.Context, string, []byte) error { return nil }
