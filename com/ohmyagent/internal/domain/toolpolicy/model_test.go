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
	// 29 → 33: PPTX·HWPX 3개(카탈로그 드리프트 보정) + task(서브에이전트) 추가.
	assert.Len(t, ClientTools, 33)
	assert.Equal(t, "run_command", ClientTools[0].Name)
	assert.True(t, IsKnownTool("read_document"))
	assert.True(t, IsKnownTool("compress_files"))
	assert.True(t, IsKnownTool("manage_todos"))
	assert.True(t, IsKnownTool("read_pptx"))
	assert.True(t, IsKnownTool("write_pptx"))
	assert.True(t, IsKnownTool("read_hwpx"))
	assert.True(t, IsKnownTool("task"))
	assert.False(t, IsKnownTool("chat_send"))

	// 카탈로그와 tool_catalog 시드 마이그레이션이 어긋나면 어드민이 그 도구를 통제할 수 없다.
	// 여기서 개수·유일성을 지켜 드리프트를 조기에 잡는다.
	seen := map[string]bool{}
	for _, tool := range ClientTools {
		assert.NotEmpty(t, tool.Category, "%s 에 카테고리가 없다", tool.Name)
		assert.False(t, seen[tool.Name], "%s 가 카탈로그에 중복", tool.Name)
		seen[tool.Name] = true
	}
}
