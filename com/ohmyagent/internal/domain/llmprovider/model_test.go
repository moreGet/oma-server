package llmprovider

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateCommand_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cmd     CreateCommand
		wantErr bool
		wantMsg string
	}{
		{
			name:    "valid LOCAL",
			cmd:     CreateCommand{Name: "ollama", ProviderType: ProviderTypeLocal},
			wantErr: false,
		},
		{
			name:    "valid EXTERNAL",
			cmd:     CreateCommand{Name: "openai", ProviderType: ProviderTypeExternal},
			wantErr: false,
		},
		{
			name:    "empty name",
			cmd:     CreateCommand{Name: "", ProviderType: ProviderTypeLocal},
			wantErr: true,
			wantMsg: "name is required",
		},
		{
			name:    "whitespace-only name (trimmed to empty)",
			cmd:     CreateCommand{Name: "   ", ProviderType: ProviderTypeLocal},
			wantErr: true,
			wantMsg: "name is required",
		},
		{
			name:    "invalid provider_type",
			cmd:     CreateCommand{Name: "bad", ProviderType: ProviderType("REMOTE")},
			wantErr: true,
			wantMsg: "provider_type must be LOCAL or EXTERNAL",
		},
		{
			name:    "empty provider_type",
			cmd:     CreateCommand{Name: "bad", ProviderType: ProviderType("")},
			wantErr: true,
			wantMsg: "provider_type must be LOCAL or EXTERNAL",
		},
		{
			name:    "valid api_key_env name",
			cmd:     CreateCommand{Name: "openai", ProviderType: ProviderTypeExternal, Config: ProviderConfig{APIKeyEnv: "OPENAI_API_KEY"}},
			wantErr: false,
		},
		{
			name:    "api_key_env holding a key value is rejected",
			cmd:     CreateCommand{Name: "openai", ProviderType: ProviderTypeExternal, Config: ProviderConfig{APIKeyEnv: "sk-proj-abc-123"}},
			wantErr: true,
			wantMsg: "api_key_env must be an environment variable NAME (e.g. OPENAI_API_KEY), not the key value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmd
			err := cmd.Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ve *ErrValidation
			require.True(t, errors.As(err, &ve), "expected *ErrValidation, got %T", err)
			assert.Equal(t, tt.wantMsg, ve.Msg)
		})
	}
}

func TestCreateCommand_Normalize(t *testing.T) {
	cmd := CreateCommand{Name: "  ollama  ", ProviderType: ProviderTypeLocal}
	cmd.Normalize()
	assert.Equal(t, "ollama", cmd.Name)
}

// 추론 강도(reasoning_effort). 관리자 오타가 벤더의 불투명한 400 으로만 드러나지 않도록 검증한다.

func TestProviderConfig_ReasoningValidation(t *testing.T) {
	t.Run("빈 값은 미지정으로 허용", func(t *testing.T) {
		assert.NoError(t, ProviderConfig{}.Validate())
	})

	t.Run("허용 목록의 값은 통과", func(t *testing.T) {
		for _, effort := range ReasoningEfforts {
			assert.NoError(t, ProviderConfig{Reasoning: effort}.Validate(), effort)
		}
	})

	t.Run("허용 목록에 없는 값은 거부", func(t *testing.T) {
		err := ProviderConfig{Reasoning: "ultra"}.Validate()
		require.Error(t, err)
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Contains(t, ve.Msg, "reasoning")
	})
}

func TestProviderConfig_APIStyleValidation(t *testing.T) {
	t.Run("빈 값은 chat_completions 기본으로 허용", func(t *testing.T) {
		c := ProviderConfig{}
		assert.NoError(t, c.Validate())
		assert.False(t, c.UsesResponsesAPI(), "빈 값은 responses 가 아니어야 한다")
	})

	t.Run("허용 목록의 값은 통과", func(t *testing.T) {
		for _, style := range APIStyles {
			assert.NoError(t, ProviderConfig{APIStyle: style}.Validate(), style)
		}
	})

	t.Run("responses 만 UsesResponsesAPI 가 참", func(t *testing.T) {
		assert.True(t, ProviderConfig{APIStyle: APIStyleResponses}.UsesResponsesAPI())
		assert.False(t, ProviderConfig{APIStyle: APIStyleChatCompletions}.UsesResponsesAPI())
	})

	t.Run("허용 목록에 없는 값은 거부", func(t *testing.T) {
		err := ProviderConfig{APIStyle: "grpc"}.Validate()
		require.Error(t, err)
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Contains(t, ve.Msg, "api_style")
	})
}

func TestProviderConfig_ReasoningNormalize(t *testing.T) {
	// 어드민 폼/REST 입력의 공백·대문자를 흡수한다(검증 전에 정규화되어야 통과한다).
	c := ProviderConfig{Reasoning: "  XHigh  "}
	c.Normalize()
	assert.Equal(t, "xhigh", c.Reasoning)
	assert.NoError(t, c.Validate())
}
