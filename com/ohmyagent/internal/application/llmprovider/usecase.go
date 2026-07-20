// Package llmproviderapp 는 LLM Provider 관리 + 활성 어댑터 조회 유스케이스를 담는다.
// 캐시 우선 조회, DB 폴백, 어댑터 생성 조율, 무효화 트리거, 인가 게이트를 여기서 수행한다.
package llmproviderapp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Service = (*ProviderService)(nil)

// accessGate 는 auth 도메인을 직접 import 하지 않기 위한 소비자 측 최소 인터페이스다(스펙 §4.4).
// main.go 에서 authUC 를 주입한다.
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

// ProviderService 는 domainllmprovider.Service 구현이다.
type ProviderService struct {
	repo    domainllmprovider.Repository
	cache   domainllmprovider.Cache
	factory domainllmprovider.Factory
	cipher  domainllmprovider.Cipher
	gate    accessGate
}

// NewProviderService 는 의존성을 주입받아 ProviderService 를 생성한다.
func NewProviderService(
	repo domainllmprovider.Repository,
	cache domainllmprovider.Cache,
	factory domainllmprovider.Factory,
	cipher domainllmprovider.Cipher,
	gate accessGate,
) *ProviderService {
	return &ProviderService{repo: repo, cache: cache, factory: factory, cipher: cipher, gate: gate}
}

// encryptKey 는 평문 API 키를 암호화한다. 빈 값은 그대로 둔다.
// 암호화 비활성(시크릿 미설정) 시 사용자 친화 검증 에러를 반환한다.
func (s *ProviderService) encryptKey(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	enc, err := s.cipher.Encrypt(plain)
	if err != nil {
		return "", &domainllmprovider.ErrValidation{Msg: "API 키를 DB 에 저장하려면 APP_ENCRYPTION_SECRET 설정이 필요합니다(또는 api_key_env 로 환경변수명만 등록하세요)"}
	}
	return enc, nil
}

// buildAdapter 는 저장된 암호문 API 키를 복호화한 뒤 어댑터를 생성한다.
func (s *ProviderService) buildAdapter(p domainllmprovider.LLMProvider) (domainllmprovider.Adapter, error) {
	if p.Config.APIKey != "" {
		plain, err := s.cipher.Decrypt(p.Config.APIKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt provider api key: %w", err)
		}
		p.Config.APIKey = plain
	}
	return s.factory.CreateAdapter(p)
}

func (s *ProviderService) now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// List 는 전체 Provider 목록을 반환한다(조회=user, 라우트에서 게이트).
func (s *ProviderService) List(ctx context.Context, actorID string) ([]domainllmprovider.LLMProvider, error) {
	return s.repo.List(ctx)
}

// Get 은 ID 로 단일 Provider 를 조회한다.
func (s *ProviderService) Get(ctx context.Context, actorID, id string) (domainllmprovider.LLMProvider, error) {
	return s.repo.FindByID(ctx, id)
}

// Create 는 새 Provider 를 등록한다(admin↑).
func (s *ProviderService) Create(ctx context.Context, cmd domainllmprovider.CreateCommand) (domainllmprovider.LLMProvider, error) {
	if err := cmd.Validate(); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	// 직접 입력한 API 키는 암호화하여 저장한다(평문 저장 금지).
	enc, err := s.encryptKey(cmd.Config.APIKey)
	if err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	cmd.Config.APIKey = enc
	now := s.now()
	p := domainllmprovider.LLMProvider{
		ID:   uuid.NewString(),
		Name: cmd.Name,
		// 항상 비활성으로 INSERT 한다. 활성 요청이면 아래에서 Activate 로 전환한다.
		IsActive:     false,
		ProviderType: cmd.ProviderType,
		Config:       cmd.Config,
		CreatedAt:    now,
		UpdatedAt:    now,
		CreatedBy:    cmd.ActorID,
		UpdatedBy:    cmd.ActorID,
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	// is_active=true 로 그냥 INSERT 하면 기존 활성 Provider 가 살아남아 활성이 둘이 된다.
	// GetActive 는 활성이 하나임을 가정하므로, 그 상태에서는 어느 쪽이 선택될지가 사실상
	// DB 스캔 순서에 좌우된다(옛 Provider 로 요청이 나가 502 로 드러남).
	// Activate 트랜잭션("전체 비활성 → 이 건만 활성")을 태워 불변식을 유지한다.
	if cmd.IsActive {
		if err := s.repo.Activate(ctx, p.ID, now.Unix(), cmd.ActorID); err != nil {
			return domainllmprovider.LLMProvider{}, err
		}
		p.IsActive = true
		s.cache.Invalidate()
	}
	slog.Info("provider created", "event", "provider.created",
		"actor", cmd.ActorID, "provider_id", p.ID, "name", p.Name, "type", string(p.ProviderType), "active", p.IsActive)
	return p, nil
}

// UpdateConfig 는 config 를 갱신한다(admin↑). 갱신 후 캐시를 무효화한다.
func (s *ProviderService) UpdateConfig(ctx context.Context, cmd domainllmprovider.UpdateConfigCommand) (domainllmprovider.LLMProvider, error) {
	if err := cmd.Validate(); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	existing, err := s.repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	if cmd.Config.APIKey == "" {
		// 키를 다시 입력하지 않으면 기존 암호문을 보존(설정 수정 시 키 유실 방지).
		cmd.Config.APIKey = existing.Config.APIKey
	} else {
		enc, encErr := s.encryptKey(cmd.Config.APIKey)
		if encErr != nil {
			return domainllmprovider.LLMProvider{}, encErr
		}
		cmd.Config.APIKey = enc
	}
	updatedAt := s.now().Unix()
	if err := s.repo.UpdateConfig(ctx, cmd.ID, cmd.Config, updatedAt, cmd.ActorID); err != nil {
		return domainllmprovider.LLMProvider{}, err
	}
	s.cache.Invalidate()
	slog.Info("provider config updated", "event", "provider.config_updated", "actor", cmd.ActorID, "provider_id", cmd.ID)
	return s.repo.FindByID(ctx, cmd.ID)
}

// Activate 는 지정 ID 를 활성화하고(트랜잭션) 캐시를 무효화한다(admin↑).
func (s *ProviderService) Activate(ctx context.Context, cmd domainllmprovider.ActivateCommand) error {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	now := s.now().Unix()
	if err := s.repo.Activate(ctx, cmd.ID, now, cmd.ActorID); err != nil {
		return err
	}
	s.cache.Invalidate()
	slog.Info("provider activated", "event", "provider.activated", "actor", cmd.ActorID, "provider_id", cmd.ID)
	return nil
}

// Delete 는 Provider 를 삭제하고 캐시를 무효화한다(admin↑).
func (s *ProviderService) Delete(ctx context.Context, cmd domainllmprovider.DeleteCommand) error {
	if err := s.gate.RequireAdmin(ctx, cmd.ActorID); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, cmd.ID); err != nil {
		return err
	}
	s.cache.Invalidate()
	slog.Info("provider deleted", "event", "provider.deleted", "actor", cmd.ActorID, "provider_id", cmd.ID)
	return nil
}

// providerTestTimeout 은 연결 테스트 1회의 한도다.
const providerTestTimeout = 10 * time.Second

// providerTestMaxTokens 는 연결 테스트 프로브의 출력 토큰 상한이다.
//
// 비용을 아끼려면 1 이 좋지만 OpenAI Responses API 는 max_output_tokens 최소가 16 이라
// 1 을 보내면 연결 자체는 멀쩡한데 400(integer_below_min_value)으로 실패한다.
// 16 은 chat/completions·Anthropic·Gemini·Ollama 어디서도 문제되지 않는 최소 공통값이다.
const providerTestMaxTokens = 16

// TestConnection 은 지정 Provider 로 최소 질의를 보내 연결을 검증한다(admin↑).
// 성공 시 nil, 외부 호출 실패 시 ErrUpstream(또는 도메인 에러)을 반환한다.
func (s *ProviderService) TestConnection(ctx context.Context, actorID, id string) error {
	if err := s.gate.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	adapter, err := s.buildAdapter(p)
	if err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, providerTestTimeout)
	defer cancel()
	req := domainllmprovider.ChatRequest{
		Messages:  []domainllmprovider.ChatMessage{{Role: domainllmprovider.ChatRoleUser, Content: "ping"}},
		MaxTokens: providerTestMaxTokens,
	}
	if err := adapter.ChatStream(testCtx, req, func(domainllmprovider.ChatStreamChunk) error { return nil }); err != nil {
		slog.Warn("provider connection test failed", "event", "provider.test",
			"actor", actorID, "provider_id", id, "name", p.Name, "error", err)
		return err
	}
	slog.Info("provider connection test ok", "event", "provider.test",
		"actor", actorID, "provider_id", id, "name", p.Name)
	return nil
}

// GetActiveAdapter 는 캐시 → DB 폴백 → 팩토리 순으로 활성 Provider 의 어댑터를 반환한다.
func (s *ProviderService) GetActiveAdapter(ctx context.Context) (domainllmprovider.Adapter, error) {
	provider, ok := s.cache.Get()
	if !ok {
		p, err := s.repo.GetActive(ctx)
		if err != nil {
			return nil, err
		}
		s.cache.Set(p)
		provider = p
	}
	return s.buildAdapter(provider)
}
