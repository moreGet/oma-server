package toolpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveEffective(t *testing.T) {
	global := Settings{Mode: "realtime", Enabled: []string{"read_file", "grep", "write_file"}, Disabled: []string{"kill_process"}}

	t.Run("nil member → 전역 그대로", func(t *testing.T) {
		mode, enabled, disabled := ResolveEffective(global, nil)
		assert.Equal(t, "realtime", mode)
		assert.Equal(t, global.Enabled, enabled)
		assert.Equal(t, global.Disabled, disabled)
	})

	t.Run("빈 멤버 → 전역 그대로", func(t *testing.T) {
		_, enabled, disabled := ResolveEffective(global, &MemberPolicy{MemberID: "m"})
		assert.Equal(t, global.Enabled, enabled)
		assert.Equal(t, global.Disabled, disabled)
	})

	t.Run("차단은 합집합(멤버는 추가 제한만)", func(t *testing.T) {
		_, _, disabled := ResolveEffective(global, &MemberPolicy{Disabled: []string{"screenshot", "kill_process"}})
		assert.Equal(t, []string{"kill_process", "screenshot"}, disabled) // 전역 먼저 + 중복 제거
	})

	t.Run("허용은 둘 다 지정 시 교집합(멤버는 좁히기만)", func(t *testing.T) {
		// 멤버가 run_command(전역 허용목록 외)를 넣어도 확장되지 않는다.
		_, enabled, _ := ResolveEffective(global, &MemberPolicy{Enabled: []string{"read_file", "run_command"}})
		assert.Equal(t, []string{"read_file"}, enabled)
	})

	t.Run("전역 허용 비었으면 멤버 허용 도입(제한 추가)", func(t *testing.T) {
		_, enabled, _ := ResolveEffective(Settings{}, &MemberPolicy{Enabled: []string{"read_file"}})
		assert.Equal(t, []string{"read_file"}, enabled)
	})

	t.Run("모드는 전역 전용(정규화)", func(t *testing.T) {
		mode, _, _ := ResolveEffective(Settings{Mode: "weird"}, &MemberPolicy{Disabled: []string{"x"}})
		assert.Equal(t, "cached", mode)
	})
}

func TestCatalog(t *testing.T) {
	assert.Len(t, ClientTools, 26)
	assert.Len(t, ClientToolNames(), 26)
	assert.Equal(t, "run_command", ClientTools[0].Name)
	assert.True(t, IsKnownTool("read_document"))
	assert.False(t, IsKnownTool("chat_send"))
}
