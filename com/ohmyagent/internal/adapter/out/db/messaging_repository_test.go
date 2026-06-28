package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// TestMessagingRepository 는 00013 마이그레이션 + 방/멤버십/메시지 영속화 + 1:1 정준키 조회를 검증한다.
func TestMessagingRepository(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + t.TempDir() + "/m.db?_pragma=busy_timeout(5000)"
	conn, err := Open("sqlite", dsn, 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, RunMigrations(ctx, "sqlite", conn))

	repo := NewMessagingRepository(conn, "sqlite")

	// 단체 방 + 멤버.
	g := domainmessaging.Room{ID: "r1", Type: domainmessaging.RoomGroup, Name: "team", CreatedBy: "u1", CreatedAt: 100}
	require.NoError(t, repo.Create(ctx, g, []string{"u1", "u2"}))
	got, err := repo.Get(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "team", got.Name)
	assert.Equal(t, domainmessaging.RoomGroup, got.Type)

	mem, _ := repo.Members(ctx, "r1")
	assert.ElementsMatch(t, []string{"u1", "u2"}, mem)
	ok, _ := repo.IsMember(ctx, "r1", "u2")
	assert.True(t, ok)
	ok, _ = repo.IsMember(ctx, "r1", "u3")
	assert.False(t, ok)

	// 1:1 방(정준키)으로 조회.
	key := domainmessaging.DirectKey("u3", "u1") // 순서 무관 동일 키
	d := domainmessaging.Room{ID: "d1", Type: domainmessaging.RoomDirect, DirectKey: key, CreatedBy: "u1", CreatedAt: 101}
	require.NoError(t, repo.Create(ctx, d, []string{"u1", "u3"}))
	found, err := repo.FindDirect(ctx, domainmessaging.DirectKey("u1", "u3"))
	require.NoError(t, err)
	assert.Equal(t, "d1", found.ID)

	// 메시지 저장 + 최신순 조회.
	require.NoError(t, repo.Save(ctx, domainmessaging.Message{ID: "m1", RoomID: "r1", SenderID: "u1", Content: "hi", CreatedAt: 200}))
	require.NoError(t, repo.Save(ctx, domainmessaging.Message{ID: "m2", RoomID: "r1", SenderID: "u2", Content: "yo", CreatedAt: 201}))
	msgs, err := repo.List(ctx, "r1", 10, "")
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "m2", msgs[0].ID) // 최신 먼저
	assert.Equal(t, "yo", msgs[0].Content)

	// before 페이지네이션: m2 이전 → m1 만.
	older, err := repo.List(ctx, "r1", 10, "m2")
	require.NoError(t, err)
	require.Len(t, older, 1)
	assert.Equal(t, "m1", older[0].ID)

	// 안읽음: m1(u1,200)·m2(u2,201). u2 는 m1(남이 보냄) 1건, u1 은 m2 1건.
	unread, _ := repo.UnreadByRoom(ctx, "u2")
	assert.Equal(t, 1, unread["r1"])
	unread1, _ := repo.UnreadByRoom(ctx, "u1")
	assert.Equal(t, 1, unread1["r1"])

	// u2 가 201 까지 읽음 → 안읽음 0.
	require.NoError(t, repo.MarkRead(ctx, "r1", "u2", 201))
	unread2, _ := repo.UnreadByRoom(ctx, "u2")
	assert.Equal(t, 0, unread2["r1"])

	// 읽음 위치는 단조 증가(더 작은 값으로 되돌아가지 않음).
	require.NoError(t, repo.MarkRead(ctx, "r1", "u2", 100))
	states, err := repo.ReadStates(ctx, "r1")
	require.NoError(t, err)
	reads := map[string]int64{}
	for _, st := range states {
		reads[st.MemberID] = st.LastReadAt
	}
	assert.Equal(t, int64(201), reads["u2"]) // 100 으로 안 내려감
	assert.Equal(t, int64(0), reads["u1"])

	// 멤버 추가(idempotent: 이미 멤버 u2 는 무시) + 나가기.
	require.NoError(t, repo.AddMembers(ctx, "r1", []string{"u4", "u2"}, 300))
	mem2, _ := repo.Members(ctx, "r1")
	assert.ElementsMatch(t, []string{"u1", "u2", "u4"}, mem2)
	require.NoError(t, repo.RemoveMember(ctx, "r1", "u4"))
	mem3, _ := repo.Members(ctx, "r1")
	assert.ElementsMatch(t, []string{"u1", "u2"}, mem3)

	// 메시지 수정.
	require.NoError(t, repo.UpdateContent(ctx, "m1", "edited", 250))
	gm, err := repo.GetMessage(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, "edited", gm.Content)
	assert.Equal(t, int64(250), gm.EditedAt)

	// 소프트 삭제: content 비고 deleted_at 기록.
	require.NoError(t, repo.MarkDeleted(ctx, "m2", 260))
	gm2, _ := repo.GetMessage(ctx, "m2")
	assert.Empty(t, gm2.Content)
	assert.Equal(t, int64(260), gm2.DeletedAt)

	// 삭제된 메시지는 수정 no-op(WHERE deleted_at=0).
	require.NoError(t, repo.UpdateContent(ctx, "m2", "x", 270))
	gm2b, _ := repo.GetMessage(ctx, "m2")
	assert.Empty(t, gm2b.Content)

	// 없는 메시지.
	_, err = repo.GetMessage(ctx, "nope")
	assert.ErrorIs(t, err, domainmessaging.ErrMessageNotFound)

	// 멘션 + 첨부 round-trip.
	require.NoError(t, repo.Save(ctx, domainmessaging.Message{
		ID: "m3", RoomID: "r1", SenderID: "u1", Content: "ping @u2", CreatedAt: 300,
		Mentions:    []string{"u2"},
		Attachments: []domainmessaging.Attachment{{FileName: "f.pdf", ContentType: "application/pdf", SizeBytes: 9, URL: "http://x/f.pdf"}},
	}))
	gm3, _ := repo.GetMessage(ctx, "m3")
	assert.Equal(t, []string{"u2"}, gm3.Mentions)
	require.Len(t, gm3.Attachments, 1)
	assert.Equal(t, "f.pdf", gm3.Attachments[0].FileName)

	// Mentioning: u2 가 멘션된 m3(삭제된 m2 는 제외).
	ment, _ := repo.Mentioning(ctx, "u2", 10)
	require.Len(t, ment, 1)
	assert.Equal(t, "m3", ment[0].ID)

	// CoMembers(u1): r1{u1,u2} + d1{u1,u3} → {u1,u2,u3}.
	co, _ := repo.CoMembers(ctx, "u1")
	assert.ElementsMatch(t, []string{"u1", "u2", "u3"}, co)

	// 멤버의 방 목록(그룹 + 1:1).
	rooms, err := repo.ListForMember(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, rooms, 2)

	// 없는 방.
	_, err = repo.Get(ctx, "nope")
	assert.ErrorIs(t, err, domainmessaging.ErrRoomNotFound)
}
