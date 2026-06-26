package projectapp

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

type fakeProjects struct{}

func (fakeProjects) UpsertProject(_ context.Context, p domainproject.Project) (domainproject.Project, error) {
	return p, nil
}
func (fakeProjects) ListProjects(context.Context, string) ([]domainproject.Project, error) {
	return nil, nil
}
func (fakeProjects) GetProject(_ context.Context, _, id string) (domainproject.Project, error) {
	return domainproject.Project{ID: id}, nil
}
func (fakeProjects) DeleteProject(context.Context, string, string) error { return nil }

type fakeConvs struct {
	count    int
	existing map[string]string // ownerID|clientID -> id
	saved    *domainproject.Conversation
}

func (f *fakeConvs) UpsertConversation(_ context.Context, c domainproject.Conversation) (domainproject.Conversation, error) {
	f.saved = &c
	return c, nil
}
func (f *fakeConvs) ListByProject(context.Context, string, string) ([]domainproject.Conversation, error) {
	return nil, nil
}
func (f *fakeConvs) CountByOwner(context.Context, string) (int, error) { return f.count, nil }
func (f *fakeConvs) FindIDByClient(_ context.Context, owner, client string) (string, bool, error) {
	id, ok := f.existing[owner+"|"+client]
	return id, ok, nil
}
func (f *fakeConvs) DeleteConversation(context.Context, string, string) error { return nil }

type fakeContent struct{ saved map[string][]byte }

func (f *fakeContent) Save(_ context.Context, key string, data []byte) error {
	f.saved[key] = data
	return nil
}

func newSvc(convs *fakeConvs, content *fakeContent, max int) *Service {
	return NewService(fakeProjects{}, convs, content, func(context.Context, string) (int, error) { return max, nil })
}

func TestService_SessionCapRejectsNew(t *testing.T) {
	convs := &fakeConvs{count: 2, existing: map[string]string{}}
	svc := newSvc(convs, &fakeContent{saved: map[string][]byte{}}, 2) // 이미 2개, 캡 2 → 신규 거부
	_, err := svc.UpsertConversation(context.Background(), domainproject.UpsertConversationCommand{
		OwnerID: "u1", ClientID: "new", Title: "T", Messages: []byte(`[{"role":"user","content":"hi"}]`), MessageCount: 1,
	})
	assert.ErrorIs(t, err, domainproject.ErrSessionLimitExceeded)
}

func TestService_ExistingUpsertAllowedOverCap(t *testing.T) {
	convs := &fakeConvs{count: 5, existing: map[string]string{"u1|s1": "cv1"}}
	svc := newSvc(convs, &fakeContent{saved: map[string][]byte{}}, 2) // 캡 초과 상태여도 기존 세션 업데이트는 허용
	c, err := svc.UpsertConversation(context.Background(), domainproject.UpsertConversationCommand{
		OwnerID: "u1", ClientID: "s1", Title: "T", Messages: []byte(`[]`),
	})
	require.NoError(t, err)
	assert.Equal(t, "cv1", c.ID)
}

func TestService_ContentStoredGzipped(t *testing.T) {
	content := &fakeContent{saved: map[string][]byte{}}
	svc := newSvc(&fakeConvs{existing: map[string]string{}}, content, 0) // 무제한
	_, err := svc.UpsertConversation(context.Background(), domainproject.UpsertConversationCommand{
		OwnerID: "u1", ProjectID: "p1", ClientID: "s9", Title: "T", Messages: []byte(`[{"role":"user","content":"안녕"}]`), MessageCount: 1,
	})
	require.NoError(t, err)
	require.Len(t, content.saved, 1)
	for key, blob := range content.saved {
		assert.Contains(t, key, "u1/p1/")
		assert.Contains(t, key, ".json.gz")
		zr, err := gzip.NewReader(bytes.NewReader(blob))
		require.NoError(t, err)
		raw, _ := io.ReadAll(zr)
		assert.Contains(t, string(raw), "안녕")
	}
}
