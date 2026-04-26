package llm

import (
	"context"
	"fmt"
	"os"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// ClaudeAdapter 는 EXTERNAL/Claude 용 어댑터이다 (Anthropic API 대상).
// 시크릿은 DB 가 아니라 환경변수에서 읽는다 (config.APIKeyEnv 가 환경변수 이름).
type ClaudeAdapter struct {
	endpoint  string
	model     string
	apiKeyEnv string
}

// NewClaudeAdapter 는 도메인 ProviderConfig 로부터 ClaudeAdapter 를 생성한다.
func NewClaudeAdapter(config domain.ProviderConfig) *ClaudeAdapter {
	return &ClaudeAdapter{
		endpoint:  config.Endpoint,
		model:     config.Model,
		apiKeyEnv: config.APIKeyEnv,
	}
}

// Complete 는 prompt 에 대한 완성을 반환한다. (현재 스텁)
func (a *ClaudeAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	// 시크릿은 항상 런타임에 환경변수에서 읽는다.
	apiKey := ""
	if a.apiKeyEnv != "" {
		apiKey = os.Getenv(a.apiKeyEnv)
	}
	keyState := "missing"
	if apiKey != "" {
		keyState = "present"
	}
	// TODO: 실제 Anthropic Messages API 호출 구현.
	return fmt.Sprintf("Claude(%s, key=%s) response for prompt: %s", a.model, keyState, prompt), nil
}

// ProviderType 은 EXTERNAL 을 반환한다.
func (a *ClaudeAdapter) ProviderType() domain.ProviderType {
	return domain.ProviderTypeExternal
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.LLMAdapter = (*ClaudeAdapter)(nil)
