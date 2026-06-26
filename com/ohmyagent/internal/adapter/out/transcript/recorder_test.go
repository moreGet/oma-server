package transcriptout

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

type fakeStore struct {
	mu    sync.Mutex
	saved []domaintranscript.Transcript
	gate  chan struct{} // nil = 즉시 저장, 아니면 닫힐 때까지 블록
}

func (f *fakeStore) Save(_ context.Context, t domaintranscript.Transcript) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	f.saved = append(f.saved, t)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) snapshot() []domaintranscript.Transcript {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domaintranscript.Transcript(nil), f.saved...)
}

func TestAsyncRecorder_DeliversAndAssignsIDAndTime(t *testing.T) {
	fs := &fakeStore{}
	r := NewAsyncRecorder(fs, nil, 1024)

	const n = 100
	for i := 0; i < n; i++ {
		r.Record(domaintranscript.Transcript{MemberID: "m", Source: domaintranscript.SourceChat})
	}
	r.Close() // flush

	saved := fs.snapshot()
	assert.Len(t, saved, n)
	assert.Zero(t, r.Dropped())
	for _, s := range saved {
		assert.NotEmpty(t, s.ID, "Record 가 ID 를 채워야 함")
		assert.False(t, s.CreatedAt.IsZero(), "Record 가 CreatedAt 을 채워야 함")
	}
}

func TestAsyncRecorder_DropsWhenBufferFull(t *testing.T) {
	gate := make(chan struct{})
	fs := &fakeStore{gate: gate}
	r := NewAsyncRecorder(fs, nil, 1) // 워커가 첫 건에서 블록 → 버퍼(1) 초과분 드롭

	for i := 0; i < 50; i++ {
		r.Record(domaintranscript.Transcript{Source: domaintranscript.SourceChat})
	}
	assert.Positive(t, r.Dropped(), "버퍼 초과분은 드롭되어야 함")

	close(gate) // 워커 해제 후 정리
	r.Close()
}
