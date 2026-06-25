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
