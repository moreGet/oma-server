package llm

import (
	"context"
	"fmt"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// OllamaAdapter 는 LOCAL provider 용 어댑터이다 (Ollama HTTP API 대상).
// 현재는 스텁으로 구현되어 있으며, 실제 HTTP 호출 로직은 향후 마일스톤에서 채워진다.
type OllamaAdapter struct {
	endpoint string
	model    string
}

// NewOllamaAdapter 는 도메인 ProviderConfig 로부터 OllamaAdapter 를 생성한다.
func NewOllamaAdapter(config domain.ProviderConfig) *OllamaAdapter {
	return &OllamaAdapter{
		endpoint: config.Endpoint,
		model:    config.Model,
	}
}

// Complete 는 prompt 에 대한 완성을 반환한다. (현재 스텁)
func (a *OllamaAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	// TODO: 실제 Ollama HTTP /api/generate 호출 구현.
	return fmt.Sprintf("Ollama(%s) response for prompt: %s", a.model, prompt), nil
}

// ProviderType 은 LOCAL 을 반환한다.
func (a *OllamaAdapter) ProviderType() domain.ProviderType {
	return domain.ProviderTypeLocal
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.LLMAdapter = (*OllamaAdapter)(nil)
