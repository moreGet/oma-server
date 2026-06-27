package httpin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

// TestQuotaExceededMapsToDetailed429 는 chat/agent 에러 매핑이 쿼터 초과를
// 429 + 상세 메시지(윈도우·used/limit·리셋)로 변환하는지 검증한다.
func TestQuotaExceededMapsToDetailed429(t *testing.T) {
	qe := &domainquota.ExceededError{
		Window: domainquota.Daily, Used: 1115, Limit: 5,
		Period: "2026-06-27", ResetUTC: time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC),
	}
	cases := map[string]error{"chat": chatErrToHTTP(qe), "agent": agentErrToHTTP(qe)}
	for name, mapped := range cases {
		ae := toAppError(mapped)
		if ae.HTTPStatus() != http.StatusTooManyRequests {
			t.Errorf("%s: status=%d want 429", name, ae.HTTPStatus())
		}
		if !strings.Contains(ae.Message, "daily token quota exceeded") || !strings.Contains(ae.Message, "1115 of 5") || !strings.Contains(ae.Message, "2026-06-28") {
			t.Errorf("%s: message=%q lacks detail", name, ae.Message)
		}
	}
}
