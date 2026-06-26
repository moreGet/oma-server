package web

import "testing"

// TestParsePages 는 모든 어드민 페이지 템플릿이 layout 과 함께 정상 파싱되는지 검증한다
// (parsePages 의 template.Must 가 파싱 오류 시 패닉 → 테스트 실패).
func TestParsePages(t *testing.T) {
	pages := parsePages()
	for _, name := range []string{"dashboard", "members", "providers", "account", "transcripts"} {
		if pages[name] == nil {
			t.Fatalf("페이지 템플릿 누락: %q", name)
		}
	}
}
