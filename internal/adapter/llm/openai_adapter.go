package llm

import (
	"context"
	"fmt"
	"os"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// OpenAIAdapter 는 EXTERNAL/OpenAI 용 어댑터이다 (OpenAI Chat Completions API 대상).
type OpenAIAdapter struct {
	endpoint  string
	model     string
	apiKeyEnv string
}

// NewOpenAIAdapter 는 도메인 ProviderConfig 로부터 OpenAIAdapter 를 생성한다.
func NewOpenAIAdapter(config domain.ProviderConfig) *OpenAIAdapter {
	return &OpenAIAdapter{
		endpoint:  config.Endpoint,
		model:     config.Model,
		apiKeyEnv: config.APIKeyEnv,
	}
}

// Complete 는 prompt 에 대한 완성을 반환한다. (현재 스텁)
func (a *OpenAIAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	apiKey := ""
	if a.apiKeyEnv != "" {
		apiKey = os.Getenv(a.apiKeyEnv)
	}
	keyState := "missing"
	if apiKey != "" {
		keyState = "present"
	}
	// TODO: 실제 OpenAI /v1/chat/completions 호출 구현.
	return fmt.Sprintf("OpenAI(%s, key=%s) response for prompt: %s", a.model, keyState, prompt), nil
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *OpenAIAdapter) ProviderType() domain.ProviderType {
	return domain.ProviderTypeExternal
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.LLMAdapter = (*OpenAIAdapter)(nil)
