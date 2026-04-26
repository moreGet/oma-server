package llm

import (
	"fmt"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// factory 는 도메인 LLMProvider → port.LLMAdapter 분기 생성을 담당한다.
type factory struct{}

// NewLLMFactory 는 LLMFactory 구현체를 반환한다.
func NewLLMFactory() port.LLMFactory {
	return &factory{}
}

// CreateAdapter 는 provider.ProviderType 에 따라 적절한 어댑터를 생성한다.
//
// 분기 규칙:
//   - LOCAL              → OllamaAdapter
//   - EXTERNAL & model ∈ {"claude", "claude-3", "claude-sonnet"}  → ClaudeAdapter
//   - EXTERNAL & 그 외   → OpenAIAdapter (기본 외부 모델)
func (f *factory) CreateAdapter(provider *domain.LLMProvider) (port.LLMAdapter, error) {
	if provider == nil {
		return nil, fmt.Errorf("nil provider")
	}
	switch provider.ProviderType {
	case domain.ProviderTypeLocal:
		return NewOllamaAdapter(provider.Config), nil
	case domain.ProviderTypeExternal:
		switch provider.Config.Model {
		case "claude", "claude-3", "claude-sonnet":
			return NewClaudeAdapter(provider.Config), nil
		default:
			return NewOpenAIAdapter(provider.Config), nil
		}
	default:
		return nil, fmt.Errorf("unknown provider type: %s", provider.ProviderType)
	}
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.LLMFactory = (*factory)(nil)
