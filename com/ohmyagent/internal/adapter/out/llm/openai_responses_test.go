package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// /v1/responses 경로. 검증은 직렬화 결과(JSON)로 한다 — SDK 의 omitzero 때문에
// "필드에 값을 넣었다"와 "실제로 전송된다"가 다르므로 전송 바이트가 유일한 진실이다.

func newResponsesAdapterForTest(reasoning string) *OpenAIAdapter {
	return NewOpenAIAdapter(domainllmprovider.ProviderConfig{
		Model:     "gpt-5.6-luna",
		Reasoning: reasoning,
		APIStyle:  domainllmprovider.APIStyleResponses,
	}, nil)
}

func TestResponsesAPI_SelectedByConfig(t *testing.T) {
	assert.True(t, newResponsesAdapterForTest("").useResponses)

	chat := NewOpenAIAdapter(domainllmprovider.ProviderConfig{Model: "gpt-4o-mini"}, nil)
	assert.False(t, chat.useResponses, "api_style 미지정이면 chat/completions 가 기본이어야 한다")
}

// 핵심: chat/completions 가 거부하던 "추론 + 도구" 조합이 responses 에서는 함께 실려야 한다.
func TestResponsesParams_ReasoningAndToolsCoexist(t *testing.T) {
	a := newResponsesAdapterForTest("medium")

	req := domainllmprovider.ChatRequest{
		Messages: []domainllmprovider.ChatMessage{
			{Role: domainllmprovider.ChatRoleUser, Content: "hi"},
		},
		Tools: []domainllmprovider.ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	}
	m := marshalMap(t, a.buildResponsesParams(req))

	reasoning, ok := m["reasoning"].(map[string]any)
	require.True(t, ok, "reasoning 이 실려야 한다")
	assert.Equal(t, "medium", reasoning["effort"])

	tools, ok := m["tools"].([]any)
	require.True(t, ok, "tools 가 실려야 한다")
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "read_file", tool["name"])
}

func TestResponsesParams_ReasoningOmittedWhenUnset(t *testing.T) {
	a := newResponsesAdapterForTest("")
	m := marshalMap(t, a.buildResponsesParams(domainllmprovider.ChatRequest{}))

	_, has := m["reasoning"]
	assert.False(t, has, "미지정이면 reasoning 을 보내면 안 된다")
}

// 모델은 서버 설정에서만 온다 — 요청의 model 은 무시되어야 한다.
func TestResponsesParams_ModelComesFromServerConfig(t *testing.T) {
	a := newResponsesAdapterForTest("low")
	m := marshalMap(t, a.buildResponsesParams(domainllmprovider.ChatRequest{Model: "gpt-4o-mini"}))

	assert.Equal(t, "gpt-5.6-luna", m["model"], "클라이언트가 보낸 모델이 아니라 Provider 설정 모델이어야 한다")
}

// 도구 호출은 assistant 메시지에 중첩되지 않고 형제 입력 아이템으로 평탄화되어야 한다.
func TestResponsesInput_ToolCallsAreSiblingItems(t *testing.T) {
	msgs := []domainllmprovider.ChatMessage{
		{Role: domainllmprovider.ChatRoleSystem, Content: "sys"},
		{Role: domainllmprovider.ChatRoleUser, Content: "list files"},
		{
			Role:      domainllmprovider.ChatRoleAssistant,
			Content:   "확인했습니다",
			ToolCalls: []domainllmprovider.ToolCall{{ID: "call_1", Name: "list_dir", Arguments: `{"path":"."}`}},
		},
		{Role: domainllmprovider.ChatRoleTool, ToolCallID: "call_1", Content: "main.go"},
	}

	items := buildResponsesInput(msgs)
	// system, user, assistant 텍스트, function_call, function_call_output = 5개
	require.Len(t, items, 5)

	fc := items[3]
	require.NotNil(t, fc.OfFunctionCall, "도구 호출은 독립 function_call 아이템이어야 한다")
	assert.Equal(t, "call_1", fc.OfFunctionCall.CallID)
	assert.Equal(t, "list_dir", fc.OfFunctionCall.Name)

	out := items[4]
	require.NotNil(t, out.OfFunctionCallOutput, "도구 결과는 function_call_output 아이템이어야 한다")
	assert.Equal(t, "call_1", out.OfFunctionCallOutput.CallID)
}

// 텍스트 없는 assistant 턴(도구 호출만)은 빈 메시지 아이템을 만들지 않아야 한다.
func TestResponsesInput_ToolOnlyAssistantTurnSkipsEmptyText(t *testing.T) {
	items := buildResponsesInput([]domainllmprovider.ChatMessage{{
		Role:      domainllmprovider.ChatRoleAssistant,
		ToolCalls: []domainllmprovider.ToolCall{{ID: "c1", Name: "n", Arguments: "{}"}},
	}})

	require.Len(t, items, 1)
	assert.NotNil(t, items[0].OfFunctionCall)
}

// 포팅 최대 함정: 인자 델타는 item_id 로 오고, 클라이언트에 돌려줄 call_id 는
// output_item.added 에서만 온다. 둘을 혼동하면 도구 결과를 되돌려보낼 때 깨진다.
func TestResponsesToolCallAccumulator_MapsItemIDToCallID(t *testing.T) {
	builders := map[string]*responsesToolCallBuilder{}

	// output_item.added 로 call_id/name 등록(키는 item_id)
	builders["item_A"] = &responsesToolCallBuilder{callID: "call_zzz", name: "read_file", order: 0}
	builders["item_B"] = &responsesToolCallBuilder{callID: "call_aaa", name: "write_file", order: 1}

	// 인자 델타는 item_id 로 누적
	builders["item_A"].args.WriteString(`{"path":`)
	builders["item_A"].args.WriteString(`"a.go"}`)
	builders["item_B"].args.WriteString(`{}`)

	calls := collectResponsesToolCalls(builders)
	require.Len(t, calls, 2)

	// output_index 순서대로 방출되어야 한다(맵 순회 순서가 아니라).
	assert.Equal(t, "call_zzz", calls[0].ID)
	assert.Equal(t, "read_file", calls[0].Name)
	assert.JSONEq(t, `{"path":"a.go"}`, calls[0].Arguments)

	assert.Equal(t, "call_aaa", calls[1].ID)
	assert.Equal(t, "write_file", calls[1].Name)
}

func TestResponsesToolCallAccumulator_EmptyReturnsNil(t *testing.T) {
	assert.Nil(t, collectResponsesToolCalls(map[string]*responsesToolCallBuilder{}))
}
