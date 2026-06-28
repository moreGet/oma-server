package messagingapp

import "sync"

// Client 는 허브에 등록된 한 개의 실시간 연결(예: WebSocket)이다.
// 한 멤버가 여러 기기/탭으로 여러 Client 를 가질 수 있다. Send 는 아웃바운드 큐다.
type Client struct {
	MemberID string
	Send     chan []byte // 버퍼드. 소비가 느려 가득 차면 해당 메시지는 드롭(연결 보호).
}

// Hub 는 memberID → 연결 집합을 관리하는 인메모리 브로드캐스트 허브다(단일 인스턴스).
// 다중 인스턴스가 필요해지면 SendToMembers 뒤에 pub/sub(예: Redis)을 끼우면 된다.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*Client]struct{}
}

// NewHub 는 빈 Hub 를 생성한다.
func NewHub() *Hub {
	return &Hub{clients: make(map[string]map[*Client]struct{})}
}

// Register 는 연결을 등록한다. 멤버의 첫 연결이면(오프라인→온라인 전환) true 를 반환한다.
func (h *Hub) Register(c *Client) (wasFirst bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.clients[c.MemberID]
	if set == nil {
		set = make(map[*Client]struct{})
		h.clients[c.MemberID] = set
		wasFirst = true
	}
	set[c] = struct{}{}
	return wasFirst
}

// Unregister 는 연결을 제거하고 Send 채널을 닫는다. 마지막 연결이면(온라인→오프라인 전환) true 를 반환한다.
func (h *Hub) Unregister(c *Client) (wasLast bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.clients[c.MemberID]
	if set == nil {
		return false
	}
	if _, ok := set[c]; ok {
		delete(set, c)
		close(c.Send)
	}
	if len(set) == 0 {
		delete(h.clients, c.MemberID)
		return true
	}
	return false
}

// IsOnline 은 멤버가 하나 이상 연결돼 있는지 반환한다.
func (h *Hub) IsOnline(memberID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[memberID]) > 0
}

// OnlineAmong 은 주어진 멤버들 중 현재 온라인인 ID 만 반환한다.
func (h *Hub) OnlineAmong(memberIDs []string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(memberIDs))
	for _, id := range memberIDs {
		if len(h.clients[id]) > 0 {
			out = append(out, id)
		}
	}
	return out
}

// SendToMembers 는 주어진 멤버들의 모든 연결로 payload 를 비차단 전송한다(큐가 가득 차면 드롭).
func (h *Hub) SendToMembers(memberIDs []string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, mid := range memberIDs {
		for c := range h.clients[mid] {
			select {
			case c.Send <- payload:
			default: // 큐 포화 → 드롭(느린 소비자가 브로드캐스트를 막지 않게)
			}
		}
	}
}

// OnlineMembers 는 현재 연결된 멤버 ID 집합 크기를 반환한다(관측/테스트용).
func (h *Hub) OnlineMembers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
