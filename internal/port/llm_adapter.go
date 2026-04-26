package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// LLMAdapter 는 특정 LLM 벤더(Ollama / Claude / GPT) 한 인스턴스를 추상화한다.
type LLMAdapter interface {
	Complete(ctx context.Context, prompt string) (string, error)
	ProviderType() domain.ProviderType
}

// LLMFactory 는 도메인 Provider 정보로부터 LLMAdapter 를 인스턴스화한다.
type LLMFactory interface {
	CreateAdapter(provider *domain.LLMProvider) (LLMAdapter, error)
}

// ProviderCache 는 활성 LLMProvider 도메인 엔티티를 lock-free 로 보관한다.
// 구현체는 sync/atomic.Value 기반.
type ProviderCache interface {
	Get() *domain.LLMProvider
	Set(provider *domain.LLMProvider)
	Invalidate()
}
