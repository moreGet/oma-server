package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// TestAgentRegistryRepository 는 00026 마이그레이션 + 업서트/heartbeat/삭제/발견 프리필터를 검증한다.
func TestAgentRegistryRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/agents.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewAgentRegistryRepository(conn, "sqlite")
	now := time.Unix(1_753_000_000, 0).UTC()

	base := domainagentregistry.Agent{
		ID: "a-1", OwnerID: "m1", Name: "reviewer",
		EndpointURL:  "http://10.0.0.5:8080",
		Capabilities: []string{"code-review", "korean-nlp"},
		Tags:         []string{"prod"},
		Model:        "gpt-4o-mini", Version: "1.0.0",
		LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now,
	}

	t.Run("INSERT → 재등록 UPDATE 는 id/created_at 유지", func(t *testing.T) {
		first, err := repo.Upsert(ctx, base)
		require.NoError(t, err)
		assert.Equal(t, "a-1", first.ID)
		assert.Equal(t, []string{"code-review", "korean-nlp"}, first.Capabilities)

		again := base
		again.ID = "a-new" // 새 uuid 로 재등록해도
		again.EndpointURL = "http://10.0.0.9:9090"
		again.UpdatedAt = now.Add(time.Hour)
		saved, err := repo.Upsert(ctx, again)
		require.NoError(t, err)
		assert.Equal(t, "a-1", saved.ID, "(owner,name) 충돌 시 기존 id 유지")
		assert.Equal(t, "http://10.0.0.9:9090", saved.EndpointURL)
		assert.Equal(t, now, saved.CreatedAt, "created_at 은 최초값 유지")
	})

	t.Run("Get: 없는 id 는 ErrNotFound", func(t *testing.T) {
		got, err := repo.Get(ctx, "a-1")
		require.NoError(t, err)
		assert.Equal(t, "reviewer", got.Name)
		_, err = repo.Get(ctx, "nope")
		assert.ErrorIs(t, err, domainagentregistry.ErrNotFound)
	})

	t.Run("Touch: 소유자 스코프 + 같은 초 재-heartbeat 허용", func(t *testing.T) {
		hb := now.Add(30 * time.Second)
		require.NoError(t, repo.Touch(ctx, "a-1", "m1", hb))
		require.NoError(t, repo.Touch(ctx, "a-1", "m1", hb), "동일 값 갱신도 성공(mysql changed-rows 시맨틱 방어)")
		got, err := repo.Get(ctx, "a-1")
		require.NoError(t, err)
		assert.Equal(t, hb, got.LastHeartbeatAt)

		assert.ErrorIs(t, repo.Touch(ctx, "a-1", "m2", hb), domainagentregistry.ErrNotFound, "타 소유자")
		assert.ErrorIs(t, repo.Touch(ctx, "nope", "m1", hb), domainagentregistry.ErrNotFound)
	})

	t.Run("List: capability/tag json 프리필터 + owner/exclude", func(t *testing.T) {
		second := base
		second.ID, second.Name, second.OwnerID = "a-2", "vision-agent", "m2"
		second.Capabilities, second.Tags = []string{"vision"}, []string{"gpu"}
		_, err := repo.Upsert(ctx, second)
		require.NoError(t, err)

		all, err := repo.List(ctx, domainagentregistry.Filter{})
		require.NoError(t, err)
		assert.Len(t, all, 2)

		caps, err := repo.List(ctx, domainagentregistry.Filter{Capability: "code-review"})
		require.NoError(t, err)
		require.Len(t, caps, 1)
		assert.Equal(t, "a-1", caps[0].ID)

		tags, err := repo.List(ctx, domainagentregistry.Filter{Tag: "gpu"})
		require.NoError(t, err)
		require.Len(t, tags, 1)
		assert.Equal(t, "a-2", tags[0].ID)

		owned, err := repo.List(ctx, domainagentregistry.Filter{OwnerID: "m1"})
		require.NoError(t, err)
		require.Len(t, owned, 1)
		assert.Equal(t, "a-1", owned[0].ID)

		excl, err := repo.List(ctx, domainagentregistry.Filter{ExcludeID: "a-1"})
		require.NoError(t, err)
		require.Len(t, excl, 1)
		assert.Equal(t, "a-2", excl[0].ID)

		// LIKE 특수문자 포함 값은 프리필터를 생략하고 전량 반환(정밀 판정은 앱단 Matches).
		wild, err := repo.List(ctx, domainagentregistry.Filter{Capability: "code%"})
		require.NoError(t, err)
		assert.Len(t, wild, 2, "프리필터 생략 → 위음성 없음")
	})

	t.Run("Delete: 소유자 스코프 / DeleteByID: 어드민 강제 해제", func(t *testing.T) {
		assert.ErrorIs(t, repo.Delete(ctx, "a-1", "m2"), domainagentregistry.ErrNotFound)
		require.NoError(t, repo.Delete(ctx, "a-1", "m1"))
		assert.ErrorIs(t, repo.Delete(ctx, "a-1", "m1"), domainagentregistry.ErrNotFound)

		require.NoError(t, repo.DeleteByID(ctx, "a-2"))
		assert.ErrorIs(t, repo.DeleteByID(ctx, "a-2"), domainagentregistry.ErrNotFound)
	})

	t.Run("DeleteHeartbeatBefore: 오래된 레코드만 정리(sweeper)", func(t *testing.T) {
		old := base
		old.ID, old.Name, old.LastHeartbeatAt = "a-old", "old-agent", now.Add(-48*time.Hour)
		fresh := base
		fresh.ID, fresh.Name, fresh.LastHeartbeatAt = "a-fresh", "fresh-agent", now
		_, err := repo.Upsert(ctx, old)
		require.NoError(t, err)
		_, err = repo.Upsert(ctx, fresh)
		require.NoError(t, err)

		n, err := repo.DeleteHeartbeatBefore(ctx, now.Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		_, err = repo.Get(ctx, "a-fresh")
		assert.NoError(t, err)
	})
}

// TestA2AKeyRepository 는 00027 마이그레이션 + 활성 키 저장/조회를 검증한다.
func TestA2AKeyRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/a2a.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewA2AKeyRepository(conn)

	t.Run("활성 키 없으면 ErrNotFound(bootstrap 트리거)", func(t *testing.T) {
		_, err := repo.GetActive(ctx)
		assert.ErrorIs(t, err, domainagentregistry.ErrNotFound)
	})

	now := time.Unix(1_753_000_000, 0).UTC()
	t.Run("저장 후 활성 키 조회(암호문 그대로 왕복)", func(t *testing.T) {
		require.NoError(t, repo.Save(ctx, domainagentregistry.A2AKey{
			KID: "kid-1", PrivateKeyPEM: "enc:PRIV", PublicKeyPEM: "PUB-PEM", Active: true, CreatedAt: now,
		}))
		got, err := repo.GetActive(ctx)
		require.NoError(t, err)
		assert.Equal(t, "kid-1", got.KID)
		assert.Equal(t, "enc:PRIV", got.PrivateKeyPEM, "레포는 암복호화하지 않는다")
		assert.Equal(t, "PUB-PEM", got.PublicKeyPEM)
		assert.True(t, got.Active)
		assert.Equal(t, now, got.CreatedAt)
	})

	t.Run("비활성 키는 GetActive 에서 제외", func(t *testing.T) {
		require.NoError(t, repo.Save(ctx, domainagentregistry.A2AKey{
			KID: "kid-2", PrivateKeyPEM: "enc:PRIV2", PublicKeyPEM: "PUB2", Active: false, CreatedAt: now.Add(time.Hour),
		}))
		got, err := repo.GetActive(ctx)
		require.NoError(t, err)
		assert.Equal(t, "kid-1", got.KID)
	})
}
