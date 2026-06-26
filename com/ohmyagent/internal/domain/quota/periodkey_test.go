package quota

import (
	"fmt"
	"testing"
	"time"
)

func TestPeriodKey(t *testing.T) {
	tm := time.Date(2026, 6, 27, 10, 30, 0, 0, time.UTC)
	if got := PeriodKey(Daily, tm); got != "2026-06-27" {
		t.Errorf("daily=%q", got)
	}
	if got := PeriodKey(Monthly, tm); got != "2026-06" {
		t.Errorf("monthly=%q", got)
	}
	y, w := tm.ISOWeek()
	if got := PeriodKey(Weekly, tm); got != fmt.Sprintf("%d-W%02d", y, w) {
		t.Errorf("weekly=%q (want %d-W%02d)", got, y, w)
	}
	// UTC 변환 확인(다른 타임존 입력도 같은 키).
	loc := time.FixedZone("KST", 9*3600)
	if got := PeriodKey(Daily, time.Date(2026, 6, 27, 2, 0, 0, 0, loc)); got != "2026-06-26" {
		t.Errorf("tz-normalized daily=%q want 2026-06-26", got)
	}
}

func TestLimits_Get(t *testing.T) {
	l := Limits{Daily: 1, Weekly: 2, Monthly: 3}
	if l.Get(Daily) != 1 || l.Get(Weekly) != 2 || l.Get(Monthly) != 3 {
		t.Errorf("Get mismatch: %+v", l)
	}
}
