package agentregistry

import (
	"errors"
	"testing"
	"time"
)

// TestComputeStatus 는 생존성 전이(online→stale→offline)와 경계값을 검증한다(§공유 계약).
func TestComputeStatus(t *testing.T) {
	ttl := 45 * time.Second
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		elapsed time.Duration
		want    Status
	}{
		{"방금 heartbeat", 0, StatusOnline},
		{"TTL 직전", ttl - time.Second, StatusOnline},
		{"TTL 경계(도달)", ttl, StatusStale},
		{"stale 구간", 2 * ttl, StatusStale},
		{"3×TTL 직전", 3*ttl - time.Second, StatusStale},
		{"3×TTL 경계(도달)", 3 * ttl, StatusOffline},
		{"오래 중단", 24 * time.Hour, StatusOffline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeStatus(base, base.Add(tc.elapsed), ttl); got != tc.want {
				t.Fatalf("elapsed=%v: got %q, want %q", tc.elapsed, got, tc.want)
			}
		})
	}

	t.Run("heartbeat 기록 없음(zero time)은 offline", func(t *testing.T) {
		if got := ComputeStatus(time.Time{}, base, ttl); got != StatusOffline {
			t.Fatalf("got %q, want offline", got)
		}
	})
	t.Run("TTL 미설정(0)은 offline", func(t *testing.T) {
		if got := ComputeStatus(base, base, 0); got != StatusOffline {
			t.Fatalf("got %q, want offline", got)
		}
	})
}

// TestRegisterCommandValidate 는 등록 입력 검증(endpoint URL·필수 필드·태그 정규화)을 검증한다.
func TestRegisterCommandValidate(t *testing.T) {
	valid := func() RegisterCommand {
		return RegisterCommand{OwnerID: "m1", Name: "reviewer", EndpointURL: "http://10.0.0.5:8080"}
	}

	t.Run("정상 입력 통과 + trailing slash 제거", func(t *testing.T) {
		cmd := valid()
		cmd.EndpointURL = " http://10.0.0.5:8080/ "
		if err := cmd.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cmd.EndpointURL != "http://10.0.0.5:8080" {
			t.Fatalf("endpoint not normalized: %q", cmd.EndpointURL)
		}
	})

	t.Run("사설 IP 허용(v1 SSRF 정책: 서버가 직접 접속하지 않음)", func(t *testing.T) {
		cmd := valid()
		cmd.EndpointURL = "http://192.168.0.10:8080"
		if err := cmd.Validate(); err != nil {
			t.Fatalf("private ip must be allowed in v1: %v", err)
		}
	})

	bad := []struct {
		name   string
		mutate func(*RegisterCommand)
	}{
		{"owner 누락", func(c *RegisterCommand) { c.OwnerID = "" }},
		{"name 누락", func(c *RegisterCommand) { c.Name = "  " }},
		{"endpoint 누락", func(c *RegisterCommand) { c.EndpointURL = "" }},
		{"상대 URL 거부", func(c *RegisterCommand) { c.EndpointURL = "/api/v1" }},
		{"비 http 스킴 거부", func(c *RegisterCommand) { c.EndpointURL = "ftp://x" }},
		{"호스트 없는 URL 거부", func(c *RegisterCommand) { c.EndpointURL = "http://" }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cmd := valid()
			tc.mutate(&cmd)
			var ve *ErrValidation
			if err := cmd.Validate(); !errors.As(err, &ve) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}

	t.Run("태그 정규화: 공백 정리·빈 값·중복 제거", func(t *testing.T) {
		cmd := valid()
		cmd.Capabilities = []string{" code-review ", "", "code-review", "korean-nlp"}
		if err := cmd.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"code-review", "korean-nlp"}
		if len(cmd.Capabilities) != len(want) {
			t.Fatalf("got %v, want %v", cmd.Capabilities, want)
		}
		for i := range want {
			if cmd.Capabilities[i] != want[i] {
				t.Fatalf("got %v, want %v", cmd.Capabilities, want)
			}
		}
	})
}

// TestFilterMatches 는 발견 필터의 순수 판정(생존성 제외)을 검증한다.
func TestFilterMatches(t *testing.T) {
	a := Agent{
		ID: "a1", OwnerID: "m1", Name: "Code Reviewer", Model: "gpt-4o-mini",
		Capabilities: []string{"code-review", "korean-nlp"}, Tags: []string{"prod"},
	}

	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"빈 필터", Filter{}, true},
		{"capability 일치", Filter{Capability: "code-review"}, true},
		{"capability 불일치", Filter{Capability: "vision"}, false},
		{"capability 는 정확 일치(부분 문자열 아님)", Filter{Capability: "code"}, false},
		{"tag 일치", Filter{Tag: "prod"}, true},
		{"tag 불일치", Filter{Tag: "gpu"}, false},
		{"q 는 name 부분 일치(대소문자 무시)", Filter{Query: "reviewer"}, true},
		{"q 는 capability 부분 일치", Filter{Query: "korean"}, true},
		{"q 불일치", Filter{Query: "vision"}, false},
		{"자기 자신 제외", Filter{ExcludeID: "a1"}, false},
		{"타 에이전트 exclude 는 무관", Filter{ExcludeID: "a2"}, true},
		{"owner 스코프", Filter{OwnerID: "m2"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.Matches(tc.f); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFilterValidate 는 status 필터 값 검증을 확인한다.
func TestFilterValidate(t *testing.T) {
	for _, ok := range []string{"", "online", "stale", "offline", " ONLINE "} {
		f := Filter{Status: ok}
		if err := f.Validate(); err != nil {
			t.Fatalf("status %q must be valid: %v", ok, err)
		}
	}
	f := Filter{Status: "zombie"}
	var ve *ErrValidation
	if err := f.Validate(); !errors.As(err, &ve) {
		t.Fatalf("want ErrValidation for unknown status, got %v", err)
	}
}
