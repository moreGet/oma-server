// Command api 는 OhMyAgent AI Agent 서버의 조립 루트(DI)다.
// 비즈니스 로직 금지: config 로드 → 어댑터 생성 → 유스케이스 주입 → 핸들러 → 라우트 등록
// → 미들웨어 체인 → graceful shutdown 만 수행한다(스펙 §3.7).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	httpin "aiagent/com/ohmyagent/internal/adapter/in/http"
	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	authout "aiagent/com/ohmyagent/internal/adapter/out/auth"
	dbout "aiagent/com/ohmyagent/internal/adapter/out/db"
	llmout "aiagent/com/ohmyagent/internal/adapter/out/llm"
	agentapp "aiagent/com/ohmyagent/internal/application/agent"
	authapp "aiagent/com/ohmyagent/internal/application/auth"
	chatapp "aiagent/com/ohmyagent/internal/application/chat"
	chatsessionapp "aiagent/com/ohmyagent/internal/application/chatsession"
	llmproviderapp "aiagent/com/ohmyagent/internal/application/llmprovider"
	"aiagent/com/ohmyagent/internal/config"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	"aiagent/com/ohmyagent/internal/logger"
)

const (
	// shutdownTimeout: graceful shutdown 시 진행 중 요청 대기 한도.
	shutdownTimeout = 15 * time.Second
	// seedTimeout: 비운영 시딩 작업 한도.
	seedTimeout = 10 * time.Second
	// defaultSeedAdminUsername: 시드 admin 사용자명 미설정 시 기본값.
	defaultSeedAdminUsername = "admin"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server terminated", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// 1) config + logger
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.Init(logger.Options{Level: "info", Format: "json"})
	log.Info("starting server", "env", cfg.Env, "addr", cfg.ServerAddr())

	// 2) DB + 마이그레이션
	conn, err := dbout.Open(cfg.Database.Driver, cfg.Database.DSN, cfg.Database.MaxOpenConns)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err := dbout.RunMigrations(migCtx, cfg.Database.Driver, conn); err != nil {
		return err
	}

	// 3) out 어댑터(repositories / hasher / token / cache / factory)
	roleRepo := dbout.NewRoleRepository(conn)
	memberRepo := dbout.NewMemberRepository(conn)
	providerRepo := dbout.NewLLMProviderRepository(conn)
	sessionRepo := dbout.NewChatSessionRepository(conn)
	hasher := authout.NewBcryptHasher(0)
	tokenSvc := security.NewJWTTokenService(cfg.Auth.JWTSecret, cfg.Auth.JWTExpiry.Std())
	providerCache := llmout.NewCache()
	providerFactory := llmout.NewFactory()

	// 4) 유스케이스(auth → provider 에 accessGate 주입)
	authUC := authapp.NewAuthUseCase(memberRepo, roleRepo, hasher, tokenSvc)
	providerUC := llmproviderapp.NewProviderService(providerRepo, providerCache, providerFactory, authUC)
	chatUC := chatapp.NewChatService(providerUC)    // providerUC 가 활성 어댑터 resolver 를 충족
	agentUC := agentapp.NewAgentService(providerUC) // 에이전트 루프(tools/function-calling) 중계
	sessionUC := chatsessionapp.NewSessionService(sessionRepo)

	// 5) 비운영 환경 시딩
	if cfg.SeedsInitialAdmin() {
		seedCtx, seedCancel := context.WithTimeout(context.Background(), seedTimeout)
		seedInitialAdmin(seedCtx, log, cfg, memberRepo, hasher, providerRepo)
		seedCancel()
	}

	// 6) 핸들러
	authH := httpin.NewAuthHandler(authUC)
	provH := httpin.NewProviderHandler(providerUC)
	chatH := httpin.NewChatHandler(chatUC)
	agentH := httpin.NewAgentHandler(agentUC)
	healthH := httpin.NewHealthHandler(conn)
	modelsH := httpin.NewModelsHandler(providerUC)
	suggestionH := httpin.NewSuggestionHandler()
	sessionH := httpin.NewSessionHandler(sessionUC)

	// 7) 라우트 등록(설계 §7)
	router := security.NewSecureRouter(http.NewServeMux(), tokenSvc)

	router.Public("POST /api/v1/auth/login", httpin.Handle(authH.Login))

	router.Secured("GET /api/v1/members", httpin.Handle(authH.ListMembers), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/members", httpin.Handle(authH.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}", httpin.Handle(authH.GetMember), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/members/{id}/role", httpin.Handle(authH.ChangeRole), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/active", httpin.Handle(authH.SetActive), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/members/{id}", httpin.Handle(authH.DeleteMember), security.MinRole(domainauth.RoleLevelSuperAdmin))

	router.Secured("GET /api/v1/llm-providers", httpin.Handle(provH.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/llm-providers/{id}", httpin.Handle(provH.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/llm-providers", httpin.Handle(provH.Create), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PATCH /api/v1/llm-providers/{id}/config", httpin.Handle(provH.UpdateConfig), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/llm-providers/{id}/activate", httpin.Handle(provH.Activate), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/llm-providers/{id}", httpin.Handle(provH.Delete), security.MinRole(domainauth.RoleLevelAdmin))

	// 질의(클라이언트 → 활성 LLM → SSE 응답). 인증된 사용자(user↑) 누구나.
	router.Secured("POST /api/v1/chat", httpin.Handle(chatH.Stream), security.MinRole(domainauth.RoleLevelUser))

	// --- C# 에이전트 클라이언트 계약(API_CONTRACT) ---
	// 에러 envelope 은 클라이언트 계약대로 { "error": { code, message } } (HandleAgent).
	router.Public("GET /api/v1/health", httpin.HandleAgent(healthH.Check)) // 헬스/연결 체크(인증 불필요)

	router.Secured("GET /api/v1/models", httpin.HandleAgent(modelsH.List), security.MinRole(domainauth.RoleLevelUser))

	// 에이전트 루프의 심장: 대화기록 + 도구스키마 → SSE(텍스트/도구호출/stop_reason).
	router.Secured("POST /api/v1/agent/chat", httpin.HandleAgent(agentH.Chat), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agent/suggestions", httpin.HandleAgent(suggestionH.List), security.MinRole(domainauth.RoleLevelUser))

	// 채팅 히스토리 서버 동기화(소유권 스코프).
	router.Secured("GET /api/v1/agent/sessions", httpin.HandleAgent(sessionH.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Upsert), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Delete), security.MinRole(domainauth.RoleLevelUser))

	// 8) 미들웨어 체인 + 서버
	handler := security.Chain(router.Mux(), cfg.Security.AllowedOrigins)
	srv := &http.Server{
		Addr:         cfg.ServerAddr(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout.Std(),
		WriteTimeout: cfg.Server.WriteTimeout.Std(),
	}

	// 9) graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		log.Info("server stopped gracefully")
		return nil
	}
}

// seedInitialAdmin 은 비운영 환경에서 super_admin 멤버와 샘플 Provider 를 생성한다.
// 이미 존재하면(중복) 무시한다. 실패는 로깅만 하고 기동을 막지 않는다(편의 기능).
func seedInitialAdmin(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	members domainauth.Repository,
	hasher domainauth.PasswordHasher,
	providers domainllmprovider.Repository,
) {
	username := cfg.Auth.SeedAdminUsername
	if username == "" {
		username = defaultSeedAdminUsername
	}
	password := cfg.Auth.SeedAdminPassword
	if password == "" {
		log.Warn("seed admin skipped: APP_AUTH_SEED_ADMIN_PASSWORD not set")
	} else if _, err := members.FindByUsername(ctx, username); errors.Is(err, domainauth.ErrNotFound) {
		hash, herr := hasher.Hash(password)
		if herr != nil {
			log.Error("seed admin: hash failed", "error", herr)
		} else {
			now := time.Now().UTC().Truncate(time.Second)
			admin := domainauth.Member{
				ID:           uuid.NewString(),
				Username:     username,
				PasswordHash: hash,
				Active:       true,
				Role:         domainauth.Role{ID: domainauth.RoleIDSuperAdmin, Name: domainauth.NameForRoleID(domainauth.RoleIDSuperAdmin), Level: domainauth.RoleLevelSuperAdmin},
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := members.Save(ctx, admin); err != nil {
				log.Error("seed admin: save failed", "error", err)
			} else {
				log.Info("seed admin created", "username", username)
			}
		}
	}

	// 샘플 Provider(활성). 이미 존재하면 무시.
	if _, err := providers.GetActive(ctx); errors.Is(err, domainllmprovider.ErrNoActiveProvider) {
		now := time.Now().UTC().Truncate(time.Second)
		sample := domainllmprovider.LLMProvider{
			ID:           uuid.NewString(),
			Name:         "local-ollama",
			IsActive:     true,
			ProviderType: domainllmprovider.ProviderTypeLocal,
			Config:       domainllmprovider.ProviderConfig{Endpoint: "http://localhost:11434", Model: "llama3"},
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := providers.Save(ctx, sample); err != nil {
			log.Error("seed provider: save failed", "error", err)
		} else {
			log.Info("seed provider created", "name", sample.Name)
		}
	}
}
