// Package cache_test 는 atomic.Value 기반 ProviderCache 의 단위 테스트이다.
// 정상 Set/Get, Invalidate, 동시성을 검증한다 (`go test -race`).
package cache_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"OhMyAgent.AiAgent.Server/internal/adapter/cache"
	"OhMyAgent.AiAgent.Server/internal/domain"
)

func TestProviderCache_GetEmpty(t *testing.T) {
	c := cache.NewProviderCache()
	assert.Nil(t, c.Get())
}

func TestProviderCache_SetAndGet(t *testing.T) {
	c := cache.NewProviderCache()
	p := &domain.LLMProvider{ID: 1, Name: "p1", ProviderType: domain.ProviderTypeLocal}
	c.Set(p)
	got := c.Get()
	assert.NotNil(t, got)
	assert.Same(t, p, got)
}

func TestProviderCache_Invalidate(t *testing.T) {
	c := cache.NewProviderCache()
	c.Set(&domain.LLMProvider{ID: 1})
	c.Invalidate()
	assert.Nil(t, c.Get())
	// 두 번 호출해도 panic 없음 (typed nil 적재 검증).
	c.Invalidate()
	assert.Nil(t, c.Get())
}

func TestProviderCache_SetAfterInvalidate(t *testing.T) {
	c := cache.NewProviderCache()
	c.Invalidate()
	p := &domain.LLMProvider{ID: 7, Name: "after"}
	c.Set(p)
	assert.Same(t, p, c.Get())
}

// TestProviderCache_ConcurrentAccess 는 race detector 와 함께 실행해야 의미가 있다.
//
//	go test -race ./internal/adapter/cache/...
func TestProviderCache_ConcurrentAccess(t *testing.T) {
	c := cache.NewProviderCache()
	const goroutines = 10
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// writers (Set)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.Set(&domain.LLMProvider{ID: int64(id*1000 + j), Name: "x"})
			}
		}(i)
	}
	// readers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = c.Get()
			}
		}()
	}
	// invalidators
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				c.Invalidate()
			}
		}()
	}
	wg.Wait()
	// panic 없이 종료되면 OK; 최종 상태는 nil 또는 LLMProvider 모두 가능.
	got := c.Get()
	if got != nil {
		assert.IsType(t, &domain.LLMProvider{}, got)
	}
}
