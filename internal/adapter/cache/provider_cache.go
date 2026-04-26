package cache

import (
	"sync/atomic"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/port"
)

// providerCache 는 sync/atomic.Value 기반 lock-free 캐시이다.
// 활성 LLMProvider 한 인스턴스를 보관하며, 모든 읽기 경로는 락 없이 동작한다.
type providerCache struct {
	value atomic.Value // *domain.LLMProvider (typed nil 도 허용)
}

// NewProviderCache 는 빈 ProviderCache 를 생성한다.
func NewProviderCache() port.ProviderCache {
	c := &providerCache{}
	// atomic.Value 는 한 번이라도 동일 타입으로 Store 되어야 후속 Store 가 안전하다.
	// nil 인 typed pointer 를 미리 적재하여 Invalidate 호출도 안전하게 만든다.
	c.value.Store((*domain.LLMProvider)(nil))
	return c
}

// Get 은 현재 캐시된 provider 를 반환한다. 캐시가 비어있으면 nil 을 반환한다.
func (c *providerCache) Get() *domain.LLMProvider {
	v := c.value.Load()
	if v == nil {
		return nil
	}
	p, ok := v.(*domain.LLMProvider)
	if !ok {
		return nil
	}
	return p
}

// Set 은 새 provider 로 원자적으로 교체한다.
func (c *providerCache) Set(provider *domain.LLMProvider) {
	c.value.Store(provider)
}

// Invalidate 는 캐시를 비운다. 다음 Get 호출은 nil 을 반환한다.
//
// 주의: atomic.Value.Store(nil) 은 panic 을 일으킨다.
// 따라서 typed nil ((*domain.LLMProvider)(nil)) 을 적재한다.
func (c *providerCache) Invalidate() {
	c.value.Store((*domain.LLMProvider)(nil))
}

// 컴파일 타임 인터페이스 만족 검증.
var _ port.ProviderCache = (*providerCache)(nil)
