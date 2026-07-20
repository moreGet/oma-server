package llmproviderapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeRepo implements domainllmprovider.Repository.
type fakeRepo struct {
	active        domainllmprovider.LLMProvider
	activeErr     error
	byID          map[string]domainllmprovider.LLMProvider
	saved         []domainllmprovider.LLMProvider
	saveErr       error
	updateCfgErr  error
	updateCfgCall int
	activateErr   error
	activateCall  int
	deleteErr     error
	deleteCall    int
	getActiveCall int
}

var _ domainllmprovider.Repository = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[string]domainllmprovider.LLMProvider{}}
}

func (r *fakeRepo) GetActive(ctx context.Context) (domainllmprovider.LLMProvider, error) {
	r.getActiveCall++
	if r.activeErr != nil {
		return domainllmprovider.LLMProvider{}, r.activeErr
	}
	return r.active, nil
}

func (r *fakeRepo) FindByID(ctx context.Context, id string) (domainllmprovider.LLMProvider, error) {
	p, ok := r.byID[id]
	if !ok {
		return domainllmprovider.LLMProvider{}, domainllmprovider.ErrNotFound
	}
	return p, nil
}

func (r *fakeRepo) List(ctx context.Context) ([]domainllmprovider.LLMProvider, error) {
	out := make([]domainllmprovider.LLMProvider, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, p)
	}
	return out, nil
}

func (r *fakeRepo) Save(ctx context.Context, p domainllmprovider.LLMProvider) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, p)
	r.byID[p.ID] = p
	return nil
}

func (r *fakeRepo) UpdateConfig(ctx context.Context, id string, cfg domainllmprovider.ProviderConfig, updatedAt int64, updatedBy string) error {
	r.updateCfgCall++
	if r.updateCfgErr != nil {
		return r.updateCfgErr
	}
	p := r.byID[id]
	p.ID = id
	p.Config = cfg
	r.byID[id] = p
	return nil
}

func (r *fakeRepo) Activate(ctx context.Context, id string, now int64, actorID string) error {
	r.activateCall++
	return r.activateErr
}

func (r *fakeRepo) Delete(ctx context.Context, id string) error {
	r.deleteCall++
	return r.deleteErr
}

// fakeCache implements domainllmprovider.Cache.
type fakeCache struct {
	entry          domainllmprovider.LLMProvider
	present        bool
	setCalls       int
	invalidateCall int
}

var _ domainllmprovider.Cache = (*fakeCache)(nil)

func (c *fakeCache) Get() (domainllmprovider.LLMProvider, bool) { return c.entry, c.present }
func (c *fakeCache) Set(p domainllmprovider.LLMProvider) {
	c.setCalls++
	c.entry = p
	c.present = true
}
func (c *fakeCache) Invalidate() {
	c.invalidateCall++
	c.present = false
}

// fakeAdapter implements domainllmprovider.Adapter.
type fakeAdapter struct {
	pt      domainllmprovider.ProviderType
	lastReq domainllmprovider.ChatRequest
}

var _ domainllmprovider.Adapter = (*fakeAdapter)(nil)

func (a *fakeAdapter) ChatStream(ctx context.Context, req domainllmprovider.ChatRequest, onChunk func(domainllmprovider.ChatStreamChunk) error) error {
	a.lastReq = req
	return onChunk(domainllmprovider.ChatStreamChunk{Done: true})
}
func (a *fakeAdapter) ProviderType() domainllmprovider.ProviderType { return a.pt }

// fakeFactory implements domainllmprovider.Factory.
type fakeFactory struct {
	lastProvider domainllmprovider.LLMProvider
	lastAdapter  *fakeAdapter
	calls        int
	err          error
}

var _ domainllmprovider.Factory = (*fakeFactory)(nil)

func (f *fakeFactory) CreateAdapter(p domainllmprovider.LLMProvider) (domainllmprovider.Adapter, error) {
	f.calls++
	f.lastProvider = p
	if f.err != nil {
		return nil, f.err
	}
	f.lastAdapter = &fakeAdapter{pt: p.ProviderType}
	return f.lastAdapter, nil
}

// noopCipher implements domainllmprovider.Cipher as identity (test only):
// Encrypt/Decrypt return the input unchanged so round-trips are predictable.
type noopCipher struct{}

var _ domainllmprovider.Cipher = (*noopCipher)(nil)

func (noopCipher) Encrypt(s string) (string, error) { return s, nil }
func (noopCipher) Decrypt(s string) (string, error) { return s, nil }

// fakeGate implements the unexported accessGate.
type fakeGate struct{ err error }

func (g *fakeGate) RequireAdmin(ctx context.Context, actorID string) error { return g.err }

var errDenied = errors.New("denied")

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestProviderService_Create(t *testing.T) {
	ctx := context.Background()

	t.Run("gate denial blocks create", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{err: errDenied})

		_, err := svc.Create(ctx, domainllmprovider.CreateCommand{
			Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal, ActorID: "a",
		})
		assert.ErrorIs(t, err, errDenied)
		assert.Empty(t, repo.saved)
	})

	t.Run("validation runs before gate", func(t *testing.T) {
		repo := newFakeRepo()
		svc := NewProviderService(repo, &fakeCache{}, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		_, err := svc.Create(ctx, domainllmprovider.CreateCommand{Name: "", ProviderType: domainllmprovider.ProviderTypeLocal, ActorID: "a"})
		var ve *domainllmprovider.ErrValidation
		assert.True(t, errors.As(err, &ve))
		assert.Empty(t, repo.saved)
	})

	t.Run("success fills uuid, timestamps, audit fields", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		p, err := svc.Create(ctx, domainllmprovider.CreateCommand{
			Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal, IsActive: false, ActorID: "admin1",
		})
		require.NoError(t, err)
		assert.NotEmpty(t, p.ID)
		assert.Equal(t, "ollama", p.Name)
		assert.False(t, p.CreatedAt.IsZero())
		assert.False(t, p.UpdatedAt.IsZero())
		assert.Equal(t, "admin1", p.CreatedBy)
		assert.Equal(t, "admin1", p.UpdatedBy)
		require.Len(t, repo.saved, 1)
		// inactive → no invalidate
		assert.Equal(t, 0, cache.invalidateCall)
	})

	t.Run("active create invalidates cache", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		_, err := svc.Create(ctx, domainllmprovider.CreateCommand{
			Name: "ollama", ProviderType: domainllmprovider.ProviderTypeLocal, IsActive: true, ActorID: "admin1",
		})
		require.NoError(t, err)
		assert.Equal(t, 1, cache.invalidateCall)
	})

	// 회귀: is_active=true 로 그대로 INSERT 하면 기존 활성 Provider 가 살아남아 활성이 둘이 된다.
	// GetActive 는 활성이 하나임을 가정하므로, 그 상태에서는 어느 쪽이 뽑힐지가 DB 스캔 순서에
	// 좌우된다(실제로 옛 Provider 로 요청이 나가 502 로 드러났다).
	// 따라서 비활성으로 INSERT 한 뒤 Activate 트랜잭션으로 배타 활성화해야 한다.
	t.Run("active create goes through Activate so only one provider stays active", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		p, err := svc.Create(ctx, domainllmprovider.CreateCommand{
			Name: "openai", ProviderType: domainllmprovider.ProviderTypeExternal, IsActive: true, ActorID: "admin1",
		})
		require.NoError(t, err)

		// INSERT 자체는 비활성이어야 한다(활성 전환은 Activate 트랜잭션의 책임).
		require.Len(t, repo.saved, 1)
		assert.False(t, repo.saved[0].IsActive, "INSERT 는 비활성이어야 배타 활성화가 보장된다")

		// 배타 활성화 트랜잭션을 정확히 1회 태워야 한다.
		assert.Equal(t, 1, repo.activateCall)

		// 호출자에게는 활성으로 보여야 한다.
		assert.True(t, p.IsActive)
	})

	t.Run("activate failure surfaces and leaves cache untouched", func(t *testing.T) {
		repo := newFakeRepo()
		repo.activateErr = errors.New("activate boom")
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		_, err := svc.Create(ctx, domainllmprovider.CreateCommand{
			Name: "openai", ProviderType: domainllmprovider.ProviderTypeExternal, IsActive: true, ActorID: "admin1",
		})
		require.Error(t, err)
		assert.Equal(t, 0, cache.invalidateCall)
	})
}

// ---------------------------------------------------------------------------
// TestConnection
// ---------------------------------------------------------------------------

func TestProviderService_TestConnection(t *testing.T) {
	ctx := context.Background()

	newSvcWithProvider := func() (*ProviderService, *fakeFactory) {
		repo := newFakeRepo()
		repo.byID["p1"] = domainllmprovider.LLMProvider{
			ID: "p1", Name: "openai", ProviderType: domainllmprovider.ProviderTypeExternal,
		}
		factory := &fakeFactory{}
		return NewProviderService(repo, &fakeCache{}, factory, &noopCipher{}, &fakeGate{}), factory
	}

	// 회귀: 프로브가 MaxTokens=1 이었다. OpenAI Responses API 는 max_output_tokens 최소가
	// 16 이라, 연결은 멀쩡한데 400(integer_below_min_value)으로 실패했다.
	t.Run("프로브 max_tokens 는 Responses 최소값 이상이어야 한다", func(t *testing.T) {
		svc, factory := newSvcWithProvider()

		require.NoError(t, svc.TestConnection(ctx, "admin1", "p1"))

		require.NotNil(t, factory.lastAdapter)
		assert.GreaterOrEqual(t, factory.lastAdapter.lastReq.MaxTokens, 16,
			"Responses API 의 max_output_tokens 최소값(16) 이상이어야 400 이 나지 않는다")
	})

	t.Run("gate 거부 시 어댑터를 만들지 않는다", func(t *testing.T) {
		repo := newFakeRepo()
		repo.byID["p1"] = domainllmprovider.LLMProvider{ID: "p1"}
		factory := &fakeFactory{}
		svc := NewProviderService(repo, &fakeCache{}, factory, &noopCipher{}, &fakeGate{err: errDenied})

		require.Error(t, svc.TestConnection(ctx, "a", "p1"))
		assert.Equal(t, 0, factory.calls)
	})

	t.Run("없는 provider 는 ErrNotFound", func(t *testing.T) {
		svc, _ := newSvcWithProvider()
		err := svc.TestConnection(ctx, "admin1", "없음")
		assert.ErrorIs(t, err, domainllmprovider.ErrNotFound)
	})
}

// ---------------------------------------------------------------------------
// UpdateConfig
// ---------------------------------------------------------------------------

func TestProviderService_UpdateConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("gate denial blocks update", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{err: errDenied})

		_, err := svc.UpdateConfig(ctx, domainllmprovider.UpdateConfigCommand{ID: "p1", ActorID: "a"})
		assert.ErrorIs(t, err, errDenied)
		assert.Equal(t, 0, repo.updateCfgCall)
		assert.Equal(t, 0, cache.invalidateCall)
	})

	t.Run("success invalidates cache and refetches", func(t *testing.T) {
		repo := newFakeRepo()
		repo.byID["p1"] = domainllmprovider.LLMProvider{ID: "p1", Name: "x"}
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		p, err := svc.UpdateConfig(ctx, domainllmprovider.UpdateConfigCommand{
			ID:      "p1",
			Config:  domainllmprovider.ProviderConfig{Model: "m"},
			ActorID: "admin1",
		})
		require.NoError(t, err)
		assert.Equal(t, "p1", p.ID)
		assert.Equal(t, "m", p.Config.Model)
		assert.Equal(t, 1, repo.updateCfgCall)
		assert.Equal(t, 1, cache.invalidateCall)
	})
}

// ---------------------------------------------------------------------------
// Activate / Delete
// ---------------------------------------------------------------------------

func TestProviderService_Activate(t *testing.T) {
	ctx := context.Background()

	t.Run("gate denial blocks activate", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{err: errDenied})

		err := svc.Activate(ctx, domainllmprovider.ActivateCommand{ID: "p1", ActorID: "a"})
		assert.ErrorIs(t, err, errDenied)
		assert.Equal(t, 0, repo.activateCall)
		assert.Equal(t, 0, cache.invalidateCall)
	})

	t.Run("success invalidates cache", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		err := svc.Activate(ctx, domainllmprovider.ActivateCommand{ID: "p1", ActorID: "admin1"})
		require.NoError(t, err)
		assert.Equal(t, 1, repo.activateCall)
		assert.Equal(t, 1, cache.invalidateCall)
	})

	t.Run("repo error does not invalidate", func(t *testing.T) {
		repo := newFakeRepo()
		repo.activateErr = domainllmprovider.ErrNotFound
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		err := svc.Activate(ctx, domainllmprovider.ActivateCommand{ID: "p1", ActorID: "admin1"})
		assert.ErrorIs(t, err, domainllmprovider.ErrNotFound)
		assert.Equal(t, 0, cache.invalidateCall)
	})
}

func TestProviderService_Delete(t *testing.T) {
	ctx := context.Background()

	t.Run("gate denial blocks delete", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{err: errDenied})

		err := svc.Delete(ctx, domainllmprovider.DeleteCommand{ID: "p1", ActorID: "a"})
		assert.ErrorIs(t, err, errDenied)
		assert.Equal(t, 0, repo.deleteCall)
		assert.Equal(t, 0, cache.invalidateCall)
	})

	t.Run("success invalidates cache", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{}
		svc := NewProviderService(repo, cache, &fakeFactory{}, &noopCipher{}, &fakeGate{})

		err := svc.Delete(ctx, domainllmprovider.DeleteCommand{ID: "p1", ActorID: "admin1"})
		require.NoError(t, err)
		assert.Equal(t, 1, repo.deleteCall)
		assert.Equal(t, 1, cache.invalidateCall)
	})
}

// ---------------------------------------------------------------------------
// GetActiveAdapter — cache hit/miss flow
// ---------------------------------------------------------------------------

func TestProviderService_GetActiveAdapter(t *testing.T) {
	ctx := context.Background()

	t.Run("cache hit skips repo and uses cached provider", func(t *testing.T) {
		repo := newFakeRepo()
		cache := &fakeCache{entry: domainllmprovider.LLMProvider{ID: "p1", ProviderType: domainllmprovider.ProviderTypeLocal}, present: true}
		factory := &fakeFactory{}
		svc := NewProviderService(repo, cache, factory, &noopCipher{}, &fakeGate{})

		ad, err := svc.GetActiveAdapter(ctx)
		require.NoError(t, err)
		assert.Equal(t, domainllmprovider.ProviderTypeLocal, ad.ProviderType())
		assert.Equal(t, 0, repo.getActiveCall)
		assert.Equal(t, 1, factory.calls)
		assert.Equal(t, "p1", factory.lastProvider.ID)
		assert.Equal(t, 0, cache.setCalls)
	})

	t.Run("cache miss loads from repo, sets cache, then factory", func(t *testing.T) {
		repo := newFakeRepo()
		repo.active = domainllmprovider.LLMProvider{ID: "p2", ProviderType: domainllmprovider.ProviderTypeExternal}
		cache := &fakeCache{present: false}
		factory := &fakeFactory{}
		svc := NewProviderService(repo, cache, factory, &noopCipher{}, &fakeGate{})

		ad, err := svc.GetActiveAdapter(ctx)
		require.NoError(t, err)
		assert.Equal(t, domainllmprovider.ProviderTypeExternal, ad.ProviderType())
		assert.Equal(t, 1, repo.getActiveCall)
		assert.Equal(t, 1, cache.setCalls)
		assert.Equal(t, "p2", cache.entry.ID)
		assert.Equal(t, 1, factory.calls)
		assert.Equal(t, "p2", factory.lastProvider.ID)
	})

	t.Run("cache miss with no active provider returns error", func(t *testing.T) {
		repo := newFakeRepo()
		repo.activeErr = domainllmprovider.ErrNoActiveProvider
		cache := &fakeCache{present: false}
		factory := &fakeFactory{}
		svc := NewProviderService(repo, cache, factory, &noopCipher{}, &fakeGate{})

		_, err := svc.GetActiveAdapter(ctx)
		assert.ErrorIs(t, err, domainllmprovider.ErrNoActiveProvider)
		assert.Equal(t, 0, cache.setCalls)
		assert.Equal(t, 0, factory.calls)
	})
}
