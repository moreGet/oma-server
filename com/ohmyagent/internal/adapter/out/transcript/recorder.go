// Package transcriptout 는 대화 이력의 비차단 기록기(out 어댑터)를 담는다.
// 핸들러는 Record 로 enqueue 만 하고, 워커가 Store 로 위임 저장한다(요청 핫패스 비차단).
package transcriptout

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// saveTimeout 은 저장 1건의 한도다(스토어 백엔드 응답 지연 대비).
const saveTimeout = 10 * time.Second

// 컴파일 타임 인터페이스 만족 검증.
var _ domaintranscript.Recorder = (*AsyncRecorder)(nil)

// AsyncRecorder 는 버퍼 채널 + 백그라운드 워커로 Store 에 비차단 위임하는 Recorder 다.
// 버퍼가 가득 차면 드롭하고 카운터를 올린다(백프레셔: 사용자 지연 대신 이력 손실).
type AsyncRecorder struct {
	store   domaintranscript.Store
	enabled func() bool // nil 이면 항상 활성. 비활성 시 enqueue 자체를 건너뜀
	ch      chan domaintranscript.Transcript
	dropped atomic.Uint64
	wg      sync.WaitGroup
	closeMu sync.Once
}

// NewAsyncRecorder 는 워커를 띄운 AsyncRecorder 를 생성한다.
// enabled 는 매 Record 호출 시 활성 여부를 알려준다(꺼져 있으면 기록을 생략한다).
func NewAsyncRecorder(store domaintranscript.Store, enabled func() bool, bufSize int) *AsyncRecorder {
	r := &AsyncRecorder{store: store, enabled: enabled, ch: make(chan domaintranscript.Transcript, bufSize)}
	r.wg.Add(1)
	go r.run()
	return r
}

func (r *AsyncRecorder) run() {
	defer r.wg.Done()
	for t := range r.ch {
		ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
		if err := r.store.Save(ctx, t); err != nil {
			slog.Warn("transcript save failed", "event", "transcript.save", "id", t.ID, "source", string(t.Source), "error", err)
		}
		cancel()
	}
}

// Record 는 활성 상태일 때 ID/CreatedAt 을 채우고 비차단으로 enqueue 한다(가득 차면 드롭).
func (r *AsyncRecorder) Record(t domaintranscript.Transcript) {
	if r.enabled != nil && !r.enabled() {
		return
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	select {
	case r.ch <- t:
	default:
		r.dropped.Add(1)
	}
}

// Dropped 는 드롭된 이력 수를 반환한다.
func (r *AsyncRecorder) Dropped() uint64 { return r.dropped.Load() }

// Close 는 채널을 닫고 워커가 잔여 이력을 모두 저장할 때까지 대기한다(graceful shutdown).
func (r *AsyncRecorder) Close() {
	r.closeMu.Do(func() {
		close(r.ch)
		r.wg.Wait()
	})
}
