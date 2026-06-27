package web

import (
	"strings"
	"testing"
)

// TestRenderMembersPage 는 멤버 페이지 템플릿이 실제 데이터로 실행(렌더)되는지 검증한다.
// 파싱만이 아니라 도넛 SVG·슬림 탭 마크업이 정상 생성되는지(필드 접근 오류 없음)까지 확인한다.
func TestRenderMembersPage(t *testing.T) {
	pages := parsePages()
	tmpl := pages["members"]
	if tmpl == nil {
		t.Fatal("members 템플릿 누락")
	}

	pd := pageData{
		Title:            "멤버",
		Active:           "members",
		User:             userView{Username: "admin", Role: "super_admin", Level: 2},
		CanManageMembers: true,
		CanDeleteMembers: true,
		Data: membersView{
			Roles: []roleView{{ID: 1, Name: "user"}, {ID: 2, Name: "admin"}},
			Members: []memberView{{
				ID: "m1", Username: "alice", Role: "user", Active: true, CreatedAt: "2026-06-27",
				DailyLimit: 1000, WeeklyLimit: 5000, MonthlyLimit: 0,
				DailyUsed: 250, WeeklyUsed: 4800, MonthlyUsed: 8400, SessionLimit: 0,
				Quota: []memberQuotaView{
					{Label: "일", Used: 250, Limit: 1000, PctUsed: 25},               // 정상
					{Label: "주", Used: 4800, Limit: 5000, PctUsed: 96},              // danger
					{Label: "월", Used: 8400, Limit: 0, Unlimited: true, PctUsed: 0}, // 무제한
				},
			}},
		},
	}

	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "layout.html", pd); err != nil {
		t.Fatalf("members 렌더 실패: %v", err)
	}
	out := sb.String()

	for _, want := range []string{
		`class="donuts donuts-sm"`,       // 목록 셀 도넛
		`class="donuts donuts-lg`,        // 요약 탭 도넛
		`stroke-dasharray="25 100"`,      // 일 25% 호
		`d-fill danger`,                  // 주 96% → danger
		`<span class="dv unl">∞</span>`,  // 월 무제한
		`data-bs-target="#tk-sum-m1"`,    // 요약 탭(첫 탭)
		`data-bs-target="#tk-token-m1"`,  // 토큰 탭
		`data-bs-target="#tk-danger-m1"`, // 삭제 탭(super_admin)
		`type="range"`,                   // 토큰 한도 슬라이더
		`name="daily_limit"`,             // 슬라이더와 짝지은 수치 입력
		`form="prof-m1"`,                 // 하단 분리된 프로필 저장 버튼
	} {
		if !strings.Contains(out, want) {
			t.Errorf("렌더 결과에 %q 누락", want)
		}
	}
}
