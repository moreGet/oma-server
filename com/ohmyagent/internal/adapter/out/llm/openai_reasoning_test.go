package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 추론 강도(reasoning_effort)는 Provider 설정에서만 오고 클라이언트는 지정할 수 없다.
// 검증은 직렬화 결과(JSON)로 한다 — SDK 의 omitzero 때문에 "필드에 값을 넣었다"와
// "실제로 전송된다"가 다르므로 전송 바이트가 유일한 진실이다(claude thinking 테스트와 같은 이유).

func newOpenAIAdapterForTest(reasoning string) *OpenAIAdapter {
	return NewOpenAIAdapter(domainllmprovider.ProviderConfig{
		Model:     "gpt-5.6-luna",
		Reasoning: reasoning,
	}, nil)
}

func TestOpenAIReasoning_EmptySendsNothing(t *testing.T) {
	a := newOpenAIAdapterForTest("")
	m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{}))

	_, has := m["reasoning_effort"]
	assert.False(t, has, "미지정이면 reasoning_effort 를 아예 보내면 안 된다(모델 기본값 유지)")
}

func TestOpenAIReasoning_ConfiguredValueIsSent(t *testing.T) {
	for _, effort := range domainllmprovider.ReasoningEfforts {
		t.Run(effort, func(t *testing.T) {
			a := newOpenAIAdapterForTest(effort)
			m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{}))

			assert.Equal(t, effort, m["reasoning_effort"])
		})
	}
}

// SDK 상수에 없는 값("max")도 문자열 그대로 실려야 한다. 서버는 모델 능력을 추측하지 않고
// 중계만 하며, 맞지 않으면 벤더가 400 으로 알려준다(ThinkingConfig 와 같은 원칙).
func TestOpenAIReasoning_UnknownToSDKValueStillRelayed(t *testing.T) {
	a := newOpenAIAdapterForTest("max")
	m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{}))

	assert.Equal(t, "max", m["reasoning_effort"])
}

// 클라이언트 요청(ChatRequest)에는 추론 강도 입력 자체가 없다. 요청 필드가 무엇이든
// 전송되는 값은 Provider 설정에서만 결정된다.
func TestOpenAIReasoning_RequestCannotOverrideProviderConfig(t *testing.T) {
	a := newOpenAIAdapterForTest("none")

	req := domainllmprovider.ChatRequest{
		Model:     "gpt-5.6-luna",
		MaxTokens: 64,
		Thinking:  &domainllmprovider.ThinkingConfig{Type: "adaptive"}, // Claude 전용, OpenAI 에는 영향 없음
	}
	m := marshalMap(t, a.buildParams(req))

	assert.Equal(t, "none", m["reasoning_effort"])
	_, hasThinking := m["thinking"]
	assert.False(t, hasThinking, "OpenAI 요청에 thinking 이 실리면 안 된다")
}

// max_tokens 는 요청값 우선, 없으면 Provider 설정값이 서버 기본값으로 쓰인다.
// (설정 필드가 저장만 되고 어댑터가 읽지 않던 데드 필드였다.)
func TestOpenAIMaxTokens_FallsBackToProviderConfig(t *testing.T) {
	a := NewOpenAIAdapter(domainllmprovider.ProviderConfig{
		Model:     "gpt-5.6-luna",
		MaxTokens: 512,
	}, nil)

	t.Run("요청 미지정이면 설정값 사용", func(t *testing.T) {
		m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{}))
		assert.EqualValues(t, 512, m["max_completion_tokens"])
	})

	t.Run("요청값이 있으면 요청값 우선", func(t *testing.T) {
		m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{MaxTokens: 64}))
		assert.EqualValues(t, 64, m["max_completion_tokens"])
	})

	t.Run("둘 다 없으면 보내지 않음", func(t *testing.T) {
		bare := NewOpenAIAdapter(domainllmprovider.ProviderConfig{Model: "m"}, nil)
		m := marshalMap(t, bare.buildParams(domainllmprovider.ChatRequest{}))
		_, has := m["max_completion_tokens"]
		assert.False(t, has)
	})
}

// responses 경로도 같은 폴백 규칙을 따라야 한다(필드명만 max_output_tokens 로 다르다).
func TestResponsesMaxTokens_FallsBackToProviderConfig(t *testing.T) {
	a := NewOpenAIAdapter(domainllmprovider.ProviderConfig{
		Model:     "gpt-5.6-luna",
		MaxTokens: 512,
		APIStyle:  domainllmprovider.APIStyleResponses,
	}, nil)

	m := marshalMap(t, a.buildResponsesParams(domainllmprovider.ChatRequest{}))
	assert.EqualValues(t, 512, m["max_output_tokens"])
}

// 모델은 서버 설정에서만 온다 — chat/completions 경로에서도 요청 model 은 무시되어야 한다.
func TestOpenAIModel_ComesFromServerConfig(t *testing.T) {
	a := NewOpenAIAdapter(domainllmprovider.ProviderConfig{Model: "gpt-5.6-luna"}, nil)
	m := marshalMap(t, a.buildParams(domainllmprovider.ChatRequest{Model: "gpt-4o-mini"}))
	assert.Equal(t, "gpt-5.6-luna", m["model"])
}

// 도구를 함께 보내도 설정된 reasoning 이 그대로 유지되는지(서버가 조합을 임의 보정하지 않음).
func TestOpenAIReasoning_KeptAlongsideTools(t *testing.T) {
	a := newOpenAIAdapterForTest("none")

	req := domainllmprovider.ChatRequest{
		Tools: []domainllmprovider.ToolDefinition{{
			Name:        "read_file",
			Description: "read a file",
			Parameters:  []byte(`{"type":"object","properties":{}}`),
		}},
	}
	m := marshalMap(t, a.buildParams(req))

	assert.Equal(t, "none", m["reasoning_effort"])
	tools, ok := m["tools"].([]any)
	require.True(t, ok, "tools 가 실려야 한다")
	assert.Len(t, tools, 1)
}
