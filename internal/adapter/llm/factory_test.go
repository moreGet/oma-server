// Package llm_test 는 LLMFactory 의 분기 로직 단위 테스트이다.
package llm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"OhMyAgent.AiAgent.Server/internal/adapter/llm"
	"OhMyAgent.AiAgent.Server/internal/domain"
)

func TestFactory_CreateLocalAdapter(t *testing.T) {
	f := llm.NewLLMFactory()
	provider := &domain.LLMProvider{
		ProviderType: domain.ProviderTypeLocal,
		Config:       domain.ProviderConfig{Endpoint: "http://localhost:11434", Model: "llama3"},
	}
	adapter, err := f.CreateAdapter(provider)
	assert.NoError(t, err)
	assert.NotNil(t, adapter)
	assert.Equal(t, domain.ProviderTypeLocal, adapter.ProviderType())
	assert.IsType(t, &llm.OllamaAdapter{}, adapter)
}

func TestFactory_CreateExternalClaude(t *testing.T) {
	f := llm.NewLLMFactory()
	tests := []string{"claude", "claude-3", "claude-sonnet"}
	for _, model := range tests {
		t.Run(model, func(t *testing.T) {
			provider := &domain.LLMProvider{
				ProviderType: domain.ProviderTypeExternal,
				Config:       domain.ProviderConfig{Model: model},
			}
			adapter, err := f.CreateAdapter(provider)
			assert.NoError(t, err)
			assert.IsType(t, &llm.ClaudeAdapter{}, adapter)
			assert.Equal(t, domain.ProviderTypeExternal, adapter.ProviderType())
		})
	}
}

func TestFactory_CreateExternalOpenAI(t *testing.T) {
	f := llm.NewLLMFactory()
	tests := []string{"gpt-4", "gpt-4o", "gpt-3.5-turbo", ""}
	for _, model := range tests {
		t.Run("model_"+model, func(t *testing.T) {
			provider := &domain.LLMProvider{
				ProviderType: domain.ProviderTypeExternal,
				Config:       domain.ProviderConfig{Model: model},
			}
			adapter, err := f.CreateAdapter(provider)
			assert.NoError(t, err)
			assert.IsType(t, &llm.OpenAIAdapter{}, adapter)
			assert.Equal(t, domain.ProviderTypeExternal, adapter.ProviderType())
		})
	}
}

func TestFactory_UnknownType(t *testing.T) {
	f := llm.NewLLMFactory()
	provider := &domain.LLMProvider{ProviderType: domain.ProviderType("WAT")}
	adapter, err := f.CreateAdapter(provider)
	assert.Error(t, err)
	assert.Nil(t, adapter)
}

func TestFactory_NilProvider(t *testing.T) {
	f := llm.NewLLMFactory()
	adapter, err := f.CreateAdapter(nil)
	assert.Error(t, err)
	assert.Nil(t, adapter)
}
