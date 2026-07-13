package logger

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// asyncBufferSize 는 비동기 로그 버퍼 채널 크기다. 초과분은 드롭된다(백프레셔).
const asyncBufferSize = 8192

// logItem 은 워커가 기록할 (inner 핸들러, 레코드) 쌍이다.
// WithAttrs/WithGroup 로 파생된 핸들러마다 inner 가 다르므로 함께 큐잉한다.
type logItem struct {
	h slog.Handler
	r slog.Record
}

// asyncSink 는 파생 핸들러들이 공유하는 비동기 상태(채널·워커·드롭 카운터)다.
// mu/closed 로 Handle 의 send 와 Close 의 close 를 직렬화한다(종료 후 동기 폴백, send-on-closed 패닉 방지).
type asyncSink struct {
	ch      chan logItem
	dropped atomic.Uint64
	wg      sync.WaitGroup
	mu      sync.RWMutex
	closed  bool
}

// asyncHandler 는 레코드를 버퍼 채널에 비차단으로 넣고 백그라운드 워커가 inner 로 기록한다(핫패스 로그 블로킹/직렬화 mutex 경합 제거).
// 버퍼가 가득 차면 레코드를 드롭하고 카운터를 올린다.
type asyncHandler struct {
	inner slog.Handler
	sink  *asyncSink
}

// newAsyncHandler 는 inner 핸들러를 감싸는 비동기 핸들러와 워커 고루틴을 만든다.
func newAsyncHandler(inner slog.Handler, bufSize int) *asyncHandler {
	s := &asyncSink{ch: make(chan logItem, bufSize)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for it := range s.ch {
			_ = it.h.Handle(context.Background(), it.r)
		}
	}()
	return &asyncHandler{inner: inner, sink: s}
}

func (h *asyncHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle 은 레코드를 복제해 채널에 비차단 enqueue 한다(가득 차면 드롭).
// 종료(Close) 후에는 채널 대신 inner 핸들러로 동기 직접 기록한다(패닉 방지).
func (h *asyncHandler) Handle(ctx context.Context, r slog.Record) error {
	h.sink.mu.RLock()
	if h.sink.closed {
		h.sink.mu.RUnlock()
		return h.inner.Handle(ctx, r)
	}
	select {
	case h.sink.ch <- logItem{h: h.inner, r: r.Clone()}:
	default:
		h.sink.dropped.Add(1)
	}
	h.sink.mu.RUnlock()
	return nil
}

func (h *asyncHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncHandler{inner: h.inner.WithAttrs(attrs), sink: h.sink}
}

func (h *asyncHandler) WithGroup(name string) slog.Handler {
	return &asyncHandler{inner: h.inner.WithGroup(name), sink: h.sink}
}

// Dropped 는 버퍼 풀로 드롭된 레코드 수를 반환한다.
func (h *asyncHandler) Dropped() uint64 { return h.sink.dropped.Load() }

// Close 는 채널을 닫고 워커가 잔여 레코드를 모두 기록할 때까지 대기한다(graceful shutdown 플러시).
// mu(쓰기락)로 진행 중인 Handle send 가 끝난 뒤 close 하여 send-on-closed 패닉을 방지한다.
func (h *asyncHandler) Close() {
	h.sink.mu.Lock()
	if h.sink.closed {
		h.sink.mu.Unlock()
		return
	}
	h.sink.closed = true
	close(h.sink.ch)
	h.sink.mu.Unlock()
	h.sink.wg.Wait()
}
