// Package llm 은 LLM Provider 의 out 포트(Factory/Cache/Adapter) 구현을 담는다.
package llm

import (
	"fmt"
	"strings"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// EXTERNAL Provider 를 모델명 접두사로 어댑터에 분기한다.
//   - claude* → ClaudeAdapter (Anthropic, 예: claude-3-5-sonnet-latest)
//   - gemini* → GeminiAdapter (Google, 예: gemini-1.5-flash)
//   - 그 외   → OpenAIAdapter (기본 외부 모델)
const (
	claudeModelPrefix = "claude"
	geminiModelPrefix = "gemini"
)

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
//   - EXTERNAL & model 이 "gemini" 로 시작 → GeminiAdapter
//   - EXTERNAL & 그 외 → OpenAIAdapter (기본 외부 모델)
func (f *Factory) CreateAdapter(provider domainllmprovider.LLMProvider) (domainllmprovider.Adapter, error) {
	switch provider.ProviderType {
	case domainllmprovider.ProviderTypeLocal:
		return NewOllamaAdapter(provider.Config), nil
	case domainllmprovider.ProviderTypeExternal:
		model := strings.ToLower(provider.Config.Model)
		switch {
		case strings.HasPrefix(model, claudeModelPrefix):
			return NewClaudeAdapter(provider.Config), nil
		case strings.HasPrefix(model, geminiModelPrefix):
			return NewGeminiAdapter(provider.Config), nil
		default:
			return NewOpenAIAdapter(provider.Config), nil
		}
	default:
		return nil, fmt.Errorf("unknown provider type: %s", provider.ProviderType)
	}
}
