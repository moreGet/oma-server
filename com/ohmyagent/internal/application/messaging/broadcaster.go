package messagingapp

// Broadcaster 는 실시간 이벤트를 대상 멤버들에게 팬아웃한다.
//   - LocalBroadcaster: 이 인스턴스의 허브로 직접 전달(단일 인스턴스).
//   - Redis 등 pub/sub 구현: 모든 인스턴스로 전파 후 각 인스턴스가 로컬 허브로 전달(다중 인스턴스).
//
// 서비스는 이 포트만 호출하므로, 전송 방식(memory/redis)을 설정으로 교체할 수 있다.
type Broadcaster interface {
	// Broadcast 는 payload(JSON 이벤트)를 memberIDs 의 연결로 전달한다(비차단·best-effort).
	Broadcast(memberIDs []string, payload []byte)
	// Close 는 백그라운드 리소스(구독 등)를 정리한다.
	Close() error
}

// LocalBroadcaster 는 단일 인스턴스용 — 로컬 허브로 직접 전달한다.
type LocalBroadcaster struct {
	hub *Hub
}

// NewLocalBroadcaster 는 LocalBroadcaster 를 생성한다.
func NewLocalBroadcaster(hub *Hub) *LocalBroadcaster {
	return &LocalBroadcaster{hub: hub}
}

// Broadcast 는 로컬 허브로 직접 전달한다.
func (b *LocalBroadcaster) Broadcast(memberIDs []string, payload []byte) {
	b.hub.SendToMembers(memberIDs, payload)
}

// Close 는 no-op 이다.
func (b *LocalBroadcaster) Close() error { return nil }
