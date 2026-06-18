package llm

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

func TestCache_GetInitiallyEmpty(t *testing.T) {
	c := NewCache()
	_, ok := c.Get()
	assert.False(t, ok)
}

func TestCache_SetThenGet(t *testing.T) {
	c := NewCache()
	want := domainllmprovider.LLMProvider{ID: "p1", Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal}
	c.Set(want)

	got, ok := c.Get()
	require.True(t, ok)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.ProviderType, got.ProviderType)
}

func TestCache_InvalidateThenGet(t *testing.T) {
	c := NewCache()
	c.Set(domainllmprovider.LLMProvider{ID: "p1"})
	_, ok := c.Get()
	require.True(t, ok)

	c.Invalidate()
	_, ok = c.Get()
	assert.False(t, ok)
}

// TestCache_Concurrent exercises the lock-free path under -race.
func TestCache_Concurrent(t *testing.T) {
	c := NewCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); c.Set(domainllmprovider.LLMProvider{ID: "x"}) }()
		go func() { defer wg.Done(); _, _ = c.Get() }()
		go func() { defer wg.Done(); c.Invalidate() }()
	}
	wg.Wait()
}
