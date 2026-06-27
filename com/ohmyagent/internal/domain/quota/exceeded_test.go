package quota

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExceededError_MatchesSentinelAndMessage(t *testing.T) {
	e := &ExceededError{Window: Daily, Used: 1115, Limit: 5, Period: "2026-06-27", ResetUTC: time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)}
	if !errors.Is(e, ErrExceeded) {
		t.Fatal("ExceededError must match ErrExceeded via errors.Is")
	}
	msg := e.Error()
	if !strings.Contains(msg, "daily token quota exceeded") || !strings.Contains(msg, "1115 of 5") || !strings.Contains(msg, "2026-06-28") {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestResetAfter(t *testing.T) {
	now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC) // 토요일
	if got := ResetAfter(Daily, now).Format("2006-01-02T15:04:05Z"); got != "2026-06-28T00:00:00Z" {
		t.Errorf("daily reset = %s, want 2026-06-28T00:00:00Z", got)
	}
	if wk := ResetAfter(Weekly, now); wk.Weekday() != time.Monday || !wk.After(now) {
		t.Errorf("weekly reset = %v, want future Monday", wk)
	}
	if got := ResetAfter(Monthly, now).Format("2006-01-02"); got != "2026-07-01" {
		t.Errorf("monthly reset = %s, want 2026-07-01", got)
	}
	// 월요일 입력 시 주간 리셋은 다음 주 월요일(+7).
	mon := time.Date(2026, 6, 29, 5, 0, 0, 0, time.UTC)
	if got := ResetAfter(Weekly, mon).Format("2006-01-02"); got != "2026-07-06" {
		t.Errorf("weekly reset from Monday = %s, want 2026-07-06", got)
	}
}
