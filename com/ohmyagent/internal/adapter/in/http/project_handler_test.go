package httpin

import (
	"net/http"
	"testing"
	"time"

	domainproject "aiagent/com/ohmyagent/internal/domain/project"
)

func TestProjectErrToHTTP(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{domainproject.ErrSessionLimitExceeded, http.StatusTooManyRequests},
		{domainproject.ErrNotFound, http.StatusNotFound},
		{&domainproject.ErrValidation{Msg: "bad"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		ae := toAppError(projectErrToHTTP(c.err))
		if ae.HTTPStatus() != c.want {
			t.Errorf("%v → %d, want %d", c.err, ae.HTTPStatus(), c.want)
		}
	}
}

func TestMessageCount(t *testing.T) {
	if n := messageCount([]byte(`[{"role":"user"},{"role":"assistant"}]`)); n != 2 {
		t.Errorf("count=%d want 2", n)
	}
	if n := messageCount(nil); n != 0 {
		t.Errorf("nil count=%d want 0", n)
	}
	if n := messageCount([]byte(`{"not":"array"}`)); n != 0 {
		t.Errorf("non-array count=%d want 0", n)
	}
}

func TestNormalizeMessages(t *testing.T) {
	if string(normalizeMessages(nil)) != "[]" {
		t.Error("nil messages should become []")
	}
	if string(normalizeMessages([]byte(`[1]`))) != "[1]" {
		t.Error("non-empty messages preserved")
	}
}

func TestParseAndFmtUTC(t *testing.T) {
	in := "2026-06-27T08:30:00Z"
	tm := parseUTC(in)
	if tm.IsZero() || fmtUTC(tm) != in {
		t.Errorf("roundtrip failed: %q → %v → %q", in, tm, fmtUTC(tm))
	}
	if !parseUTC("garbage").IsZero() {
		t.Error("invalid time should parse to zero")
	}
	if fmtUTC(time.Time{}) != "" {
		t.Error("zero time should format to empty string")
	}
}
