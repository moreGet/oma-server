package messagingbus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture 는 deliver 콜백이 받은 마지막 (memberIDs, payload)을 보관한다.
type capture struct {
	mu      sync.Mutex
	members []string
	payload []byte
	got     chan struct{}
}

func (c *capture) deliver(members []string, payload []byte) {
	c.mu.Lock()
	c.members = append([]string(nil), members...)
	c.payload = append([]byte(nil), payload...)
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
}

// TestRedisBroadcaster_MultiInstance 는 한 인스턴스에서 Broadcast 한 이벤트가
// 같은 채널을 구독하는 다른 인스턴스로 pub/sub 전파되어 로컬 전달되는지 검증한다(miniredis).
func TestRedisBroadcaster_MultiInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	opt := func() *redis.Options { return &redis.Options{Addr: mr.Addr()} }
	const channel = "test:chat"

	capA := &capture{got: make(chan struct{}, 1)}
	bcA, err := NewRedisBroadcaster(ctx, opt(), channel, capA.deliver) // 인스턴스 A
	require.NoError(t, err)
	defer func() { _ = bcA.Close() }()

	capB := &capture{got: make(chan struct{}, 1)}
	bcB, err := NewRedisBroadcaster(ctx, opt(), channel, capB.deliver) // 인스턴스 B
	require.NoError(t, err)
	defer func() { _ = bcB.Close() }()

	// 구독이 자리잡을 시간을 잠깐 준다.
	time.Sleep(100 * time.Millisecond)

	// A 에서 전송 → B(다른 인스턴스)가 받아야 함.
	bcA.Broadcast([]string{"u1", "u2"}, []byte(`{"type":"message"}`))

	select {
	case <-capB.got:
	case <-time.After(2 * time.Second):
		t.Fatal("인스턴스 B 가 A 의 Broadcast 를 받지 못함")
	}
	capB.mu.Lock()
	defer capB.mu.Unlock()
	assert.Equal(t, []string{"u1", "u2"}, capB.members)
	assert.JSONEq(t, `{"type":"message"}`, string(capB.payload))
}

// TestRedisBroadcaster_PingFails 는 접속 불가 시 (빠르게) 에러를 반환하는지 검증한다.
func TestRedisBroadcaster_PingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := NewRedisBroadcaster(ctx,
		&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 300 * time.Millisecond},
		"x", func([]string, []byte) {})
	require.Error(t, err)
}
