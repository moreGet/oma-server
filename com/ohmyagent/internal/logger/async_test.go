package logger

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHandler 는 처리된 레코드 메시지를 수집하는 테스트용 slog.Handler 다.
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, r.Message)
	c.mu.Unlock()
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }
func (c *captureHandler) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func TestAsyncHandler_DeliversAllAfterClose(t *testing.T) {
	cap := &captureHandler{}
	ah := newAsyncHandler(cap, 1024)
	l := slog.New(ah)

	const n = 200
	for i := 0; i < n; i++ {
		l.Info("msg")
	}
	ah.Close() // 플러시 + 워커 종료 대기

	assert.Equal(t, n, cap.count())
	assert.Zero(t, ah.Dropped())
}

func TestAsyncHandler_WithAttrsAndGroupDoNotPanic(t *testing.T) {
	cap := &captureHandler{}
	ah := newAsyncHandler(cap, 16)
	l := slog.New(ah).With("svc", "x").WithGroup("g")
	l.Info("hello")
	l.Info("world")
	ah.Close()
	require.Equal(t, 2, cap.count())
}

func TestAsyncHandler_LogAfterCloseDoesNotPanic(t *testing.T) {
	cap := &captureHandler{}
	ah := newAsyncHandler(cap, 16)
	l := slog.New(ah)

	l.Info("before")
	ah.Close()
	// 종료 후 로그(예: graceful shutdown 중 slog.Error)는 패닉 없이 동기 기록되어야 한다.
	l.Info("after-close")
	ah.Close() // 중복 Close 도 안전해야 함

	assert.Equal(t, 2, cap.count())
}

func TestAsyncHandler_DropsWhenBufferFull(t *testing.T) {
	// inner 가 무한 대기하도록 막아 워커를 1건에서 멈추고, 버퍼(1)를 넘긴 enqueue 는 드롭되게 한다.
	block := make(chan struct{})
	bh := &blockingHandler{gate: block}
	ah := newAsyncHandler(bh, 1)
	l := slog.New(ah)

	for i := 0; i < 50; i++ { // 워커는 1건에서 블록, 버퍼는 1 → 나머지는 드롭
		l.Info("x")
	}
	assert.Positive(t, ah.Dropped(), "버퍼 초과분은 드롭되어야 함")
	close(block) // 워커 해제 후 정리
	ah.Close()
}

// blockingHandler 는 첫 Handle 에서 gate 가 닫힐 때까지 대기한다.
type blockingHandler struct {
	gate chan struct{}
	once sync.Once
}

func (b *blockingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (b *blockingHandler) Handle(context.Context, slog.Record) error {
	b.once.Do(func() { <-b.gate })
	return nil
}
func (b *blockingHandler) WithAttrs([]slog.Attr) slog.Handler { return b }
func (b *blockingHandler) WithGroup(string) slog.Handler      { return b }
