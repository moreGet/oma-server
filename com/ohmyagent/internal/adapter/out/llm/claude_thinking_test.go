package llm

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 확장 사고. 검증은 직렬화 결과(JSON)로 한다 — SDK 의 omitzero 때문에 "필드에 값을 넣었다"와
// "실제로 전송된다"가 다르므로 전송 바이트가 유일한 진실이다(cache 테스트와 같은 이유).

func marshalMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// ── 요청 파라미터: 모델별 형식 ──

func TestClaudeThinking_NilSendsNothing(t *testing.T) {
	assert.Nil(t, claudeThinking(nil))
}

func TestClaudeThinking_AdaptiveShape(t *testing.T) {
	u := claudeThinking(&domainllmprovider.ThinkingConfig{Type: "adaptive"})
	require.NotNil(t, u)

	m := marshalMap(t, *u)
	assert.Equal(t, "adaptive", m["type"])
	// adaptive 는 budget_tokens 를 보내면 안 된다(최신 모델이 거부).
	_, hasBudget := m["budget_tokens"]
	assert.False(t, hasBudget, "adaptive 에 budget_tokens 가 실리면 안 된다")
}

func TestClaudeThinking_EnabledShape(t *testing.T) {
	u := claudeThinking(&domainllmprovider.ThinkingConfig{Type: "enabled", BudgetTokens: 4096})
	require.NotNil(t, u)

	m := marshalMap(t, *u)
	assert.Equal(t, "enabled", m["type"])
	assert.EqualValues(t, 4096, m["budget_tokens"])
}

func TestClaudeThinking_UnknownTypeIsIgnored(t *testing.T) {
	// 오타로 요청 전체가 깨지면 안 된다 — 미사용과 동일 취급.
	assert.Nil(t, claudeThinking(&domainllmprovider.ThinkingConfig{Type: "typo"}))
}

// ── 사고 블록 재생: tool_use 앞에 와야 한다(핵심) ──

func TestClaudeBuildMessages_ReplaysThinkingBeforeToolUse(t *testing.T) {
	// thinking 이 켜진 채 도구를 쓴 assistant 턴을 재생할 때, thinking 블록이 tool_use 앞에
	// "먼저" 와야 한다. 순서가 틀리거나 빠지면 Anthropic 이 400 을 돌려준다.
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleUser, Content: "파일 찾아줘"},
		{
			Role:              domainllmprovider.ChatRoleAssistant,
			Content:           "찾아보겠습니다",
			Thinking:          "먼저 glob 으로 후보를 좁히자",
			ThinkingSignature: "sig-abc123",
			ToolCalls:         []domainllmprovider.ToolCall{{ID: "c1", Name: "glob", Arguments: `{"pattern":"**/*.cs"}`}},
		},
		{Role: domainllmprovider.ChatRoleTool, ToolCallID: "c1", Content: "결과"},
	}

	out := claudeBuildMessages(msgs)

	// assistant 메시지를 찾는다.
	var assistant *anthropic.MessageParam
	for i := range out {
		if out[i].Role == anthropic.MessageParamRoleAssistant {
			assistant = &out[i]
			break
		}
	}
	require.NotNil(t, assistant)
	require.GreaterOrEqual(t, len(assistant.Content), 2)

	// 첫 블록이 thinking, 그 안에 서명이 보존돼야 한다.
	first := marshalMap(t, assistant.Content[0])
	assert.Equal(t, "thinking", first["type"], "thinking 블록이 맨 앞에 와야 한다")
	assert.Equal(t, "먼저 glob 으로 후보를 좁히자", first["thinking"])
	assert.Equal(t, "sig-abc123", first["signature"], "서명은 바이트 그대로 보존돼야 한다")

	// 어딘가에 tool_use 가 있고, thinking 이 그보다 앞이어야 한다.
	toolUseIdx := -1
	for i := range assistant.Content {
		if marshalMap(t, assistant.Content[i])["type"] == "tool_use" {
			toolUseIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, toolUseIdx, 1, "tool_use 는 thinking(0번) 뒤에 있어야 한다")
}

func TestClaudeBuildMessages_NoThinkingWhenAbsent(t *testing.T) {
	// thinking 이 없으면(미사용) 사고 블록을 만들면 안 된다 — 없는 서명으로 400 을 부른다.
	msgs := []domainllmprovider.ChatMessage{
		{
			Role:      domainllmprovider.ChatRoleAssistant,
			Content:   "답변",
			ToolCalls: []domainllmprovider.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}},
		},
	}

	out := claudeBuildMessages(msgs)
	require.Len(t, out, 1)

	for _, block := range out[0].Content {
		assert.NotEqual(t, "thinking", marshalMap(t, block)["type"], "thinking 이 없는데 블록이 생겼다")
	}
}

func TestClaudeBuildMessages_ThinkingWithoutSignatureIsSkipped(t *testing.T) {
	// 서명 없는 thinking 은 재생할 수 없다(검증 불가) — 넣으면 400 이므로 건너뛴다.
	msgs := []domainllmprovider.ChatMessage{
		{
			Role:      domainllmprovider.ChatRoleAssistant,
			Content:   "답변",
			Thinking:  "서명 없는 사고",
			ToolCalls: []domainllmprovider.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}},
		},
	}

	out := claudeBuildMessages(msgs)
	require.Len(t, out, 1)
	for _, block := range out[0].Content {
		assert.NotEqual(t, "thinking", marshalMap(t, block)["type"])
	}
}
