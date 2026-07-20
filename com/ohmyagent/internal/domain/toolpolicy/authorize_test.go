package toolpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Authorize 는 도구 인가의 단일 진실이다. /tools/authorize(실행 직전 realtime 게이트)와
// /agent/chat(요청 게이트)이 이 함수를 공유해야 판정이 갈라지지 않는다.

func TestAuthorize(t *testing.T) {
	tests := []struct {
		name        string
		enabled     []string
		disabled    []string
		tool        string
		wantAllowed bool
		wantReason  string
	}{
		{
			name: "정책 없음 = 전체 허용", tool: "anything", wantAllowed: true,
		},
		{
			name:     "disabled 에 있으면 차단",
			disabled: []string{"run_command"}, tool: "run_command",
			wantAllowed: false, wantReason: ReasonBlocked,
		},
		{
			name:     "disabled 에 없으면 허용",
			disabled: []string{"run_command"}, tool: "read_file",
			wantAllowed: true,
		},
		{
			name:    "enabled 화이트리스트 안이면 허용",
			enabled: []string{"read_file"}, tool: "read_file",
			wantAllowed: true,
		},
		{
			name:    "enabled 화이트리스트 밖이면 차단",
			enabled: []string{"read_file"}, tool: "write_file",
			wantAllowed: false, wantReason: ReasonNotInAllowlist,
		},
		{
			// 보안 하한: 화이트리스트에 있어도 disabled 가 이긴다.
			name:    "disabled 가 enabled 보다 우선",
			enabled: []string{"run_command"}, disabled: []string{"run_command"}, tool: "run_command",
			wantAllowed: false, wantReason: ReasonBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, reason := Authorize(tt.enabled, tt.disabled, tt.tool)
			assert.Equal(t, tt.wantAllowed, allowed)
			assert.Equal(t, tt.wantReason, reason)
		})
	}
}

// 전역은 보안 하한이다 — 멤버 오버라이드가 전역 차단을 풀 수 없어야 한다.
func TestResolveEffective_MemberCannotUnblockGlobal(t *testing.T) {
	global := Settings{Mode: "cached", Disabled: []string{"run_command"}}
	member := &MemberPolicy{Enabled: []string{"run_command"}}

	_, enabled, disabled := ResolveEffective(global, member)

	assert.Contains(t, disabled, "run_command", "전역 차단은 멤버가 풀 수 없다")
	allowed, reason := Authorize(enabled, disabled, "run_command")
	assert.False(t, allowed)
	assert.Equal(t, ReasonBlocked, reason)
}

// 멤버 차단은 전역에 더해진다(합집합).
func TestResolveEffective_MemberAddsToGlobalDisabled(t *testing.T) {
	global := Settings{Mode: "cached", Disabled: []string{"manage_todos"}}
	member := &MemberPolicy{Disabled: []string{"read_file"}}

	_, _, disabled := ResolveEffective(global, member)

	assert.ElementsMatch(t, []string{"manage_todos", "read_file"}, disabled)
}
