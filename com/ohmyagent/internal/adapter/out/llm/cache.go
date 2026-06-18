package llm

import (
	"sync/atomic"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Cache = (*Cache)(nil)

// cacheEntry 는 atomic.Value 에 적재되는 불변 스냅샷이다.
// atomic.Value 는 항상 동일 concrete 타입을 저장해야 하므로 래퍼를 사용한다.
type cacheEntry struct {
	provider domainllmprovider.LLMProvider
	present  bool
}

// Cache 는 sync/atomic.Value 기반 lock-free 캐시이다.
// 활성 LLMProvider 한 인스턴스를 보관하며 모든 읽기 경로는 락 없이 동작한다.
type Cache struct {
	value atomic.Value // cacheEntry
}

// NewCache 는 빈 Cache 를 생성한다.
func NewCache() *Cache {
	c := &Cache{}
	c.value.Store(cacheEntry{})
	return c
}

// Get 은 캐시된 provider 와 존재 여부를 반환한다.
func (c *Cache) Get() (domainllmprovider.LLMProvider, bool) {
	v := c.value.Load()
	e, ok := v.(cacheEntry)
	if !ok || !e.present {
		return domainllmprovider.LLMProvider{}, false
	}
	return e.provider, true
}

// Set 은 새 provider 로 원자적으로 교체한다.
func (c *Cache) Set(p domainllmprovider.LLMProvider) {
	c.value.Store(cacheEntry{provider: p, present: true})
}

// Invalidate 는 캐시를 비운다. 다음 Get 호출은 present=false 를 반환한다.
func (c *Cache) Invalidate() {
	c.value.Store(cacheEntry{})
}
