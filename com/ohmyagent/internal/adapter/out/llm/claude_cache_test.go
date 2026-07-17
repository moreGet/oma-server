package llm

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// hasCacheControl 은 실제 직렬화 결과에 cache_control 이 실렸는지 본다.
//
// 필드에 값을 대입했는지로 검사하면 안 된다: SDK 의 `omitzero` 태그 때문에 제로값 구조체는
// 대입해도 JSON 에서 빠진다(즉 캐싱이 조용히 no-op 이 된다). 전송되는 바이트가 유일한 진실이다.
func hasCacheControl(t *testing.T, v any) bool {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var probe map[string]any
	require.NoError(t, json.Unmarshal(b, &probe))
	_, ok := probe["cache_control"]
	return ok
}

// 프롬프트 캐싱. 에이전트 루프는 매 반복마다 전체 대화를 재전송하므로(서버 stateless),
// 캐시 브레이크포인트가 빠지면 그 이력을 매번 정가로 재청구받는다.

func toolDefs(n int) []domainllmprovider.ToolDefinition {
	out := make([]domainllmprovider.ToolDefinition, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domainllmprovider.ToolDefinition{
			Name:        "tool" + string(rune('a'+i)),
			Description: "설명",
			Parameters:  []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		})
	}
	return out
}

func TestClaudeBuildTools_MarksLastToolForCaching(t *testing.T) {
	tools := claudeBuildTools(toolDefs(3))
	require.Len(t, tools, 3)

	// 마지막 도구에만 브레이크포인트 — 프롬프트 순서가 system → tools → messages 이므로
	// 여기 하나로 [system + tools] 프리픽스 전체가 캐시된다.
	assert.False(t, hasCacheControl(t, tools[0].OfTool), "첫 도구엔 브레이크포인트가 없어야 한다")
	assert.False(t, hasCacheControl(t, tools[1].OfTool))
	assert.True(t, hasCacheControl(t, tools[2].OfTool), "마지막 도구에 브레이크포인트가 있어야 한다")
}

func TestClaudeBuildTools_EmptyStaysNil(t *testing.T) {
	assert.Nil(t, claudeBuildTools(nil))
}

func TestClaudeBuildSystem_CachesOnlyWhenAsked(t *testing.T) {
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleSystem, Content: "시스템 프롬프트"},
		{Role: domainllmprovider.ChatRoleUser, Content: "안녕"},
	}

	// 도구가 있는 요청: 도구 끝 브레이크포인트가 system 을 덮으므로 중복 표시 금지.
	withTools := claudeBuildSystem(msgs, false)
	require.Len(t, withTools, 1)
	assert.False(t, hasCacheControl(t, withTools[0]))

	// 도구가 없는 요청(예: 요약 전용 호출): system 을 직접 표시해야 캐시된다.
	noTools := claudeBuildSystem(msgs, true)
	require.Len(t, noTools, 1)
	assert.True(t, hasCacheControl(t, noTools[0]))
}

func TestClaudeBuildSystem_JoinsAllSystemMessages(t *testing.T) {
	// 클라이언트가 컴팩션 요약을 두 번째 system 메시지로 보낸다 — 합쳐져야 한다.
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleSystem, Content: "기본 프롬프트"},
		{Role: domainllmprovider.ChatRoleSystem, Content: "이전 대화 요약"},
		{Role: domainllmprovider.ChatRoleUser, Content: "안녕"},
	}

	got := claudeBuildSystem(msgs, false)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Text, "기본 프롬프트")
	assert.Contains(t, got[0].Text, "이전 대화 요약")
}

func TestClaudeBuildMessages_MarksLastBlockForHistoryCaching(t *testing.T) {
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleSystem, Content: "sys"},
		{Role: domainllmprovider.ChatRoleUser, Content: "첫 요청"},
		{Role: domainllmprovider.ChatRoleAssistant, Content: "답변"},
		{Role: domainllmprovider.ChatRoleUser, Content: "두 번째 요청"},
	}

	out := claudeBuildMessages(msgs)
	require.NotEmpty(t, out)

	last := out[len(out)-1]
	require.NotEmpty(t, last.Content)
	assert.True(t, hasCacheControl(t, last.Content[len(last.Content)-1]),
		"마지막 메시지 끝에 브레이크포인트가 있어야 다음 반복이 캐시 적중된다")
}

func TestClaudeBuildMessages_MarksToolResultBlock(t *testing.T) {
	// 에이전트 루프에서 가장 흔한 마지막 메시지는 tool 결과다 — 여기도 표시돼야 한다.
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleUser, Content: "요청"},
		{Role: domainllmprovider.ChatRoleAssistant, ToolCalls: []domainllmprovider.ToolCall{
			{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
		}},
		{Role: domainllmprovider.ChatRoleTool, ToolCallID: "c1", Content: "파일 내용"},
	}

	out := claudeBuildMessages(msgs)
	require.NotEmpty(t, out)

	last := out[len(out)-1]
	assert.True(t, hasCacheControl(t, last.Content[len(last.Content)-1]))
}

func TestClaudeBuildMessages_EmptyIsSafe(t *testing.T) {
	assert.NotPanics(t, func() { claudeBuildMessages(nil) })
	assert.NotPanics(t, func() { claudeMarkHistoryCache(nil) })
}

// ── 사용량 집계 ──

func TestClaudeUsage_PromptIncludesCacheTokens(t *testing.T) {
	// Anthropic 은 캐시 적중분을 input_tokens 에서 제외한다. 그대로 쓰면 캐싱을 켜는 순간
	// 쿼터 소모가 급감해 사용량 산정 기준이 조용히 바뀐다 — 캐싱은 비용 최적화이지 정책 변경이 아니다.
	got := claudeUsage(anthropic.Usage{
		InputTokens:              100,
		CacheReadInputTokens:     900,
		CacheCreationInputTokens: 50,
		OutputTokens:             30,
	})

	assert.Equal(t, 1050, got.PromptTokens, "처리된 입력 총합(신규+캐시읽기+캐시생성)이어야 한다")
	assert.Equal(t, 30, got.CompletionTokens)
	assert.Equal(t, 1080, got.TotalTokens)
	assert.Equal(t, 900, got.CacheReadTokens)
	assert.Equal(t, 50, got.CacheCreationTokens)
}

func TestClaudeUsage_NoCacheIsUnchangedFromBefore(t *testing.T) {
	// 캐시가 없던 시절과 동일한 값이어야 한다(회귀 방지).
	got := claudeUsage(anthropic.Usage{InputTokens: 500, OutputTokens: 200})

	assert.Equal(t, 500, got.PromptTokens)
	assert.Equal(t, 700, got.TotalTokens)
	assert.Zero(t, got.CacheReadTokens)
	assert.Zero(t, got.CacheCreationTokens)
}
