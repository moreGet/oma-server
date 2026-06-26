package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

// TestProjectSyncRepositories 는 00008 마이그레이션 + 프로젝트/대화 업서트 + 세션 설정/한도/블롭을 검증한다.
func TestProjectSyncRepositories(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/ps.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	now := time.Unix(1000, 0).UTC()
	pr := NewProjectRepository(conn)
	cr := NewConversationRepository(conn)

	// 프로젝트 업서트(INSERT) → 같은 client_id 재전송 시 같은 서버 id(UPDATE).
	p1, err := pr.UpsertProject(ctx, domainproject.Project{ID: "p-uuid", OwnerID: "u1", ClientID: "c1", Name: "Proj", CreatedUTC: now, UpdatedUTC: now})
	require.NoError(t, err)
	assert.Equal(t, "p-uuid", p1.ID)
	p2, err := pr.UpsertProject(ctx, domainproject.Project{ID: "other", OwnerID: "u1", ClientID: "c1", Name: "Renamed", CreatedUTC: now, UpdatedUTC: now.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "p-uuid", p2.ID, "기존 client_id 는 같은 서버 id 유지")

	list, err := pr.ListProjects(ctx, "u1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "Renamed", list[0].Name)

	// 대화 업서트 + 카운트 + 프로젝트 conversation_count.
	_, err = cr.UpsertConversation(ctx, domainproject.Conversation{ID: "cv1", ProjectID: "p-uuid", OwnerID: "u1", ClientID: "s1", Title: "T", CreatedUTC: now, UpdatedUTC: now, MessageCount: 3})
	require.NoError(t, err)
	n, _ := cr.CountByOwner(ctx, "u1")
	assert.Equal(t, 1, n)
	convs, _ := cr.ListByProject(ctx, "u1", "p-uuid")
	require.Len(t, convs, 1)
	assert.Equal(t, 3, convs[0].MessageCount)
	pg, _ := pr.GetProject(ctx, "u1", "p-uuid")
	assert.Equal(t, 1, pg.ConversationCount)
	id, ok, _ := cr.FindIDByClient(ctx, "u1", "s1")
	assert.True(t, ok)
	assert.Equal(t, "cv1", id)

	// 세션 설정(시드 db/무제한 → 변경).
	sr := NewSessionSettingsRepository(conn)
	s, _ := sr.Get(ctx)
	assert.Equal(t, domainproject.BackendDB, s.Backend)
	assert.Equal(t, 0, s.DefaultMaxSessions)
	s.Backend, s.FileDir, s.DefaultMaxSessions = domainproject.BackendFile, "/var/sessions", 5
	require.NoError(t, sr.Save(ctx, s))
	s2, _ := sr.Get(ctx)
	assert.Equal(t, domainproject.BackendFile, s2.Backend)
	assert.Equal(t, 5, s2.DefaultMaxSessions)

	// 멤버 세션 한도 + DB 블롭 upsert.
	mr := NewMemberSessionLimitRepository(conn)
	require.NoError(t, mr.Set(ctx, "u1", 3))
	v, _ := mr.Get(ctx, "u1")
	assert.Equal(t, 3, v)
	bs := NewSessionBlobStore(conn)
	require.NoError(t, bs.Save(ctx, "u1/p-uuid/cv1.json.gz", []byte("a")))
	require.NoError(t, bs.Save(ctx, "u1/p-uuid/cv1.json.gz", []byte("bb"))) // 같은 key 재저장(upsert)
}
