// Package messagingbus 는 채팅 실시간 이벤트를 인스턴스 간 전파하는 out 어댑터를 제공한다.
// RedisBroadcaster 는 Redis pub/sub 으로 다중 인스턴스(LB) 실시간 전달을 구현한다.
package messagingbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// publishTimeout 은 Broadcast 의 Redis publish 최대 대기다(느린 Redis 가 요청 고루틴을 블록하지 않게).
const publishTimeout = 2 * time.Second

// envelope 는 채널에 publish 되는 메시지다(대상 멤버 + 이벤트 페이로드).
type envelope struct {
	MemberIDs []string        `json:"member_ids"`
	Payload   json.RawMessage `json:"payload"`
}

// RedisBroadcaster 는 이벤트를 Redis 채널로 publish 하고, 구독으로 받은 이벤트를 로컬 전달 함수(deliver, 보통 hub.SendToMembers)로 전달한다.
// 모든 인스턴스가 같은 채널을 구독하며 publish 측은 로컬 전달을 직접 하지 않아 이중 전달이 없다.
type RedisBroadcaster struct {
	rdb     *redis.Client
	channel string
	deliver func(memberIDs []string, payload []byte)
	sub     *redis.PubSub
	cancel  context.CancelFunc
}

// NewRedisBroadcaster 는 Redis 에 연결을 확인(PING)하고 구독 루프를 시작한다.
// deliver 는 수신 이벤트를 로컬 연결로 전달하는 함수다(예: hub.SendToMembers).
func NewRedisBroadcaster(ctx context.Context, opt *redis.Options, channel string, deliver func([]string, []byte)) (*RedisBroadcaster, error) {
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("messagingbus: redis ping: %w", err)
	}
	subCtx, cancel := context.WithCancel(context.Background())
	b := &RedisBroadcaster{
		rdb:     rdb,
		channel: channel,
		deliver: deliver,
		sub:     rdb.Subscribe(subCtx, channel),
		cancel:  cancel,
	}
	go b.run(subCtx)
	return b, nil
}

// run 은 구독 채널을 읽어 로컬로 전달한다(종료 시 ctx 취소).
func (b *RedisBroadcaster) run(ctx context.Context) {
	ch := b.sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var env envelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				slog.Warn("messagingbus: bad envelope", "error", err)
				continue
			}
			b.deliver(env.MemberIDs, env.Payload)
		}
	}
}

// Broadcast 는 이벤트를 Redis 채널로 publish 한다(모든 인스턴스가 구독으로 받아 로컬 전달).
func (b *RedisBroadcaster) Broadcast(memberIDs []string, payload []byte) {
	data, err := json.Marshal(envelope{MemberIDs: memberIDs, Payload: payload})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := b.rdb.Publish(ctx, b.channel, data).Err(); err != nil {
		slog.Warn("messagingbus: publish failed", "error", err)
	}
}

// Close 는 구독 루프와 Redis 연결을 정리한다.
func (b *RedisBroadcaster) Close() error {
	b.cancel()
	_ = b.sub.Close()
	return b.rdb.Close()
}
