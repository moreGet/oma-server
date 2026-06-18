// Package llm 은 LLM Provider 의 out 포트(Factory/Cache/Adapter) 구현을 담는다.
package llm

import (
	"fmt"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// claudeModelPrefix 는 EXTERNAL 모델을 ClaudeAdapter 로 분기하는 모델명 접두사다.
// 실제 Anthropic 모델 ID(claude-3-5-sonnet-latest 등)를 모두 포괄한다.
const claudeModelPrefix = "claude"

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Factory = (*Factory)(nil)

// Factory 는 도메인 LLMProvider → Adapter 분기 생성을 담당한다.
type Factory struct{}

// NewFactory 는 Factory 구현체를 반환한다.
func NewFactory() *Factory { return &Factory{} }

// CreateAdapter 는 provider.ProviderType 에 따라 적절한 어댑터를 생성한다.
//
// 분기 규칙:
//   - LOCAL    → OllamaAdapter
//   - EXTERNAL & model 이 "claude" 로 시작 → ClaudeAdapter
//   - EXTERNAL & 그 외 → OpenAIAdapter (기본 외부 모델)
func (f *Factory) CreateAdapter(provider domainllmprovider.LLMProvider) (domainllmprovider.Adapter, error) {
	switch provider.ProviderType {
	case domainllmprovider.ProviderTypeLocal:
		return NewOllamaAdapter(provider.Config), nil
	case domainllmprovider.ProviderTypeExternal:
		if strings.HasPrefix(strings.ToLower(provider.Config.Model), claudeModelPrefix) {
			return NewClaudeAdapter(provider.Config), nil
		}
		return NewOpenAIAdapter(provider.Config), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", provider.ProviderType)
	}
}
