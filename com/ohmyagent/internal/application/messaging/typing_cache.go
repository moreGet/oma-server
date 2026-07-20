package messagingapp

import (
	"sync"
	"time"
)

// 타이핑 인디케이터 전용 멤버 목록 캐시.
//
// 타이핑은 키 입력마다 쏟아지는 고빈도·휘발성 신호인데, 예전에는 이벤트 1건마다
// IsMember + Members 로 DB 를 2회 때렸다. 200 명이 초당 2회 입력하면 800 q/s 가
// 순수 오버헤드로 쌓이고, sqlite 단일 커넥션에서는 그대로 병목이 된다.
//
// **이 캐시는 타이핑 전파에만 쓴다.** 메시지 전송·조회 등 실제 권한이 걸린 경로는
// 캐시를 타지 않고 계속 DB 로 검사한다. 캐시된 멤버십으로 인가를 결정하면 방에서
// 쫓겨난 뒤에도 잠시 접근이 열리기 때문이다. 타이핑은 "누가 입력 중" 이라는
// 휘발성 힌트라 짧은 지연을 감수할 수 있다.

const (
	// typingMembersTTL 은 캐시 엔트리 수명이다.
	//
	// 같은 인스턴스의 멤버십 변경(초대·강퇴·나가기)은 즉시 무효화한다. TTL 은 그 외
	// 경로에 대한 상한선이다 — 다중 인스턴스(redis broadcaster)에서는 다른 인스턴스의
	// 변경이 이 캐시에 닿지 않으므로, 최악의 경우 이 시간만큼 옛 멤버 목록으로 전파된다.
	typingMembersTTL = 5 * time.Second

	// typingCacheMaxRooms 는 엔트리 수 상한이다. 방 수만큼 무한히 늘지 않도록
	// 초과 시 만료분을 걷어낸다(그래도 넘치면 통째로 비운다 — 캐시일 뿐이라 안전하다).
	typingCacheMaxRooms = 4096
)

// memberListCache 는 roomID → 멤버 목록의 TTL 캐시다.
type memberListCache struct {
	mu      sync.RWMutex
	entries map[string]memberListEntry
	now     func() time.Time
}

type memberListEntry struct {
	members []string
	expires time.Time
}

func newMemberListCache(now func() time.Time) *memberListCache {
	if now == nil {
		now = time.Now
	}
	return &memberListCache{entries: map[string]memberListEntry{}, now: now}
}

// get 은 만료되지 않은 멤버 목록을 반환한다.
// 반환된 슬라이스는 캐시가 공유하므로 **변경하면 안 된다**(호출부는 읽기만 한다).
func (c *memberListCache) get(roomID string) ([]string, bool) {
	c.mu.RLock()
	e, ok := c.entries[roomID]
	c.mu.RUnlock()
	if !ok || c.now().After(e.expires) {
		return nil, false
	}
	return e.members, true
}

// put 은 멤버 목록을 TTL 과 함께 저장한다.
//
// 전달받은 슬라이스를 그대로 붙들지 않고 복사한다. 레포지토리가 내부 슬라이스를
// 반환하는 구현이면, 이후 그쪽에서 append/수정될 때 캐시가 조용히 함께 변형된다
// (락으로도 못 막는 aliasing 이다). 캐시 적재는 TTL 당 한 번뿐이라 복사 비용은 무시할 만하다.
func (c *memberListCache) put(roomID string, members []string) {
	cp := make([]string, len(members))
	copy(cp, members)
	members = cp

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= typingCacheMaxRooms {
		c.pruneExpiredLocked()
		if len(c.entries) >= typingCacheMaxRooms {
			c.entries = make(map[string]memberListEntry, typingCacheMaxRooms)
		}
	}
	c.entries[roomID] = memberListEntry{members: members, expires: c.now().Add(typingMembersTTL)}
}

// invalidate 는 방의 캐시를 즉시 버린다(멤버십이 바뀐 직후 호출).
func (c *memberListCache) invalidate(roomID string) {
	c.mu.Lock()
	delete(c.entries, roomID)
	c.mu.Unlock()
}

// pruneExpiredLocked 는 만료 엔트리를 제거한다(호출자가 락을 쥐고 있어야 한다).
func (c *memberListCache) pruneExpiredLocked() {
	now := c.now()
	for id, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, id)
		}
	}
}
