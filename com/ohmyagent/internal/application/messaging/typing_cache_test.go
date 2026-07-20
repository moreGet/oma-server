package messagingapp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// 타이핑은 키 입력마다 오는 고빈도 신호라 DB 왕복이 그대로 부하가 된다.
// 예전에는 이벤트 1건마다 IsMember + Members 로 2회를 때렸다.

func newTypingSvc(t *testing.T) (*Service, *fakeRepo, string) {
	t.Helper()
	repo := newFakeRepo()
	s := NewService(repo, repo, newAttStore(), NewHub(), nil)
	room, err := s.CreateGroup(context.Background(), "u1", "방", []string{"u2", "u3"})
	require.NoError(t, err)
	repo.membersCalls, repo.isMemberCalls = 0, 0 // 셋업 분 제외
	return s, repo, room.ID
}

// 타이핑 1건은 DB 조회를 최대 1회만 해야 한다(멤버십 검사는 가져온 목록으로 해결).
func TestTyping_SingleQueryPerEvent(t *testing.T) {
	s, repo, roomID := newTypingSvc(t)

	require.NoError(t, s.Typing(context.Background(), "u1", roomID, "start"))

	assert.Equal(t, 0, repo.isMemberCalls, "IsMember 를 따로 조회하면 안 된다")
	assert.LessOrEqual(t, repo.membersCalls, 1)
}

// 연속 타이핑은 캐시를 타 DB 조회가 늘지 않아야 한다.
func TestTyping_RepeatedEventsHitCache(t *testing.T) {
	s, repo, roomID := newTypingSvc(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		require.NoError(t, s.Typing(ctx, "u1", roomID, "start"))
	}

	assert.Equal(t, 1, repo.membersCalls, "20 회 타이핑에 멤버 조회는 1 회여야 한다")
	assert.Equal(t, 0, repo.isMemberCalls)
}

// 캐시를 쓰더라도 비멤버는 여전히 거부되어야 한다.
func TestTyping_NonMemberStillRejected(t *testing.T) {
	s, _, roomID := newTypingSvc(t)

	err := s.Typing(context.Background(), "stranger", roomID, "start")
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)
}

// 강퇴 즉시 캐시가 무효화되어, 쫓겨난 멤버가 타이핑을 더 받지 않아야 한다.
func TestTyping_KickInvalidatesCache(t *testing.T) {
	s, repo, roomID := newTypingSvc(t)
	ctx := context.Background()

	require.NoError(t, s.Typing(ctx, "u1", roomID, "start")) // 캐시 적재
	require.Equal(t, 1, repo.membersCalls)

	require.NoError(t, s.KickMember(ctx, "u1", roomID, "u3"))

	before := repo.membersCalls
	require.NoError(t, s.Typing(ctx, "u1", roomID, "start"))
	assert.Greater(t, repo.membersCalls, before, "강퇴 후에는 목록을 다시 읽어야 한다")

	// 쫓겨난 멤버는 더 이상 타이핑 대상이 아니다.
	members, err := s.typingRoomMembers(ctx, roomID)
	require.NoError(t, err)
	assert.NotContains(t, members, "u3")
}

// 초대 즉시 캐시가 무효화되어, 신규 멤버가 바로 타이핑을 받아야 한다.
func TestTyping_AddMembersInvalidatesCache(t *testing.T) {
	s, repo, roomID := newTypingSvc(t)
	ctx := context.Background()

	require.NoError(t, s.Typing(ctx, "u1", roomID, "start"))
	require.Equal(t, 1, repo.membersCalls)

	_, err := s.AddMembers(ctx, "u1", roomID, []string{"u9"})
	require.NoError(t, err)

	members, err := s.typingRoomMembers(ctx, roomID)
	require.NoError(t, err)
	assert.Contains(t, members, "u9", "초대 직후 신규 멤버가 목록에 있어야 한다")
}

// 나가기도 즉시 반영되어야 한다.
func TestTyping_LeaveInvalidatesCache(t *testing.T) {
	s, _, roomID := newTypingSvc(t)
	ctx := context.Background()

	require.NoError(t, s.Typing(ctx, "u1", roomID, "start"))
	require.NoError(t, s.LeaveRoom(ctx, "u2", roomID))

	members, err := s.typingRoomMembers(ctx, roomID)
	require.NoError(t, err)
	assert.NotContains(t, members, "u2")
}

// --- 캐시 자료구조 자체 ---

func TestMemberListCache_ExpiresAfterTTL(t *testing.T) {
	now := time.Now()
	c := newMemberListCache(func() time.Time { return now })

	c.put("r1", []string{"a", "b"})
	got, ok := c.get("r1")
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, got)

	now = now.Add(typingMembersTTL + time.Second)
	_, ok = c.get("r1")
	assert.False(t, ok, "TTL 이 지나면 캐시가 만료되어야 한다")
}

func TestMemberListCache_Invalidate(t *testing.T) {
	c := newMemberListCache(time.Now)
	c.put("r1", []string{"a"})
	c.invalidate("r1")
	_, ok := c.get("r1")
	assert.False(t, ok)
}

// 방 수만큼 무한히 늘지 않아야 한다(메모리 상한).
func TestMemberListCache_BoundedGrowth(t *testing.T) {
	now := time.Now()
	c := newMemberListCache(func() time.Time { return now })

	for i := 0; i < typingCacheMaxRooms+500; i++ {
		c.put(string(rune(i%1000))+"-"+time.Duration(i).String(), []string{"a"})
	}

	c.mu.RLock()
	size := len(c.entries)
	c.mu.RUnlock()
	assert.LessOrEqual(t, size, typingCacheMaxRooms, "엔트리 수가 상한을 넘으면 안 된다")
}
