// Command api 는 OhMyAgent AI Agent 서버의 조립 루트(DI)다.
// 비즈니스 로직 금지: config 로드 → 어댑터 생성 → 유스케이스 주입 → 핸들러 → 라우트 등록
// → 미들웨어 체인 → graceful shutdown 만 수행한다(스펙 §3.7).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"aiagent/com/ohmyagent/internal/adapter/in/web"
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

	// 5) 시딩: super_admin 은 모든 환경에서 항상 보장(없으면 생성). 샘플 Provider 는 비운영만.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), seedTimeout)
	ensureSuperAdmin(seedCtx, log, cfg, memberRepo, hasher)
	if cfg.SeedsInitialAdmin() {
		seedSampleProvider(seedCtx, log, providerRepo)
	}
	seedCancel()

	// 6) 핸들러
	authH := httpin.NewAuthHandler(authUC)
	provH := httpin.NewProviderHandler(providerUC)
	chatH := httpin.NewChatHandler(chatUC)
	agentH := httpin.NewAgentHandler(agentUC)
	healthH := httpin.NewHealthHandler(conn)
	modelsH := httpin.NewModelsHandler(providerUC)
	suggestionH := httpin.NewSuggestionHandler()
	sessionH := httpin.NewSessionHandler(sessionUC)
	statsH := httpin.NewStatsHandler(authUC, providerUC)
	webServer := web.NewServer(authUC, providerUC, tokenSvc, cfg.Auth.JWTExpiry.Std(), cfg.Env == "prod")

	// 7) 라우트 등록(설계 §7)
	router := security.NewSecureRouter(http.NewServeMux(), tokenSvc)

	router.Public("POST /api/v1/auth/login", httpin.Handle(authH.Login))

	// 본인 정보·비밀번호·역할목록(인증된 사용자)
	router.Secured("GET /api/v1/me", httpin.Handle(authH.Me), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/me/password", httpin.Handle(authH.ChangePassword), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/roles", httpin.Handle(authH.ListRoles), security.MinRole(domainauth.RoleLevelUser))

	router.Secured("GET /api/v1/members", httpin.Handle(authH.ListMembers), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/members", httpin.Handle(authH.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}", httpin.Handle(authH.GetMember), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/members/{id}/role", httpin.Handle(authH.ChangeRole), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/active", httpin.Handle(authH.SetActive), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/members/{id}", httpin.Handle(authH.DeleteMember), security.MinRole(domainauth.RoleLevelSuperAdmin))
	router.Secured("PUT /api/v1/members/{id}/password", httpin.Handle(authH.ResetPassword), security.MinRole(domainauth.RoleLevelAdmin))

	router.Secured("GET /api/v1/llm-providers", httpin.Handle(provH.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/llm-providers/{id}", httpin.Handle(provH.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/llm-providers", httpin.Handle(provH.Create), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PATCH /api/v1/llm-providers/{id}/config", httpin.Handle(provH.UpdateConfig), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/llm-providers/{id}/activate", httpin.Handle(provH.Activate), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/llm-providers/{id}", httpin.Handle(provH.Delete), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/llm-providers/{id}/test", httpin.Handle(provH.Test), security.MinRole(domainauth.RoleLevelAdmin))

	// 대시보드 집계(admin↑)
	router.Secured("GET /api/v1/statistics", httpin.Handle(statsH.Get), security.MinRole(domainauth.RoleLevelAdmin))

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

	// 어드민 웹 페이지(htmx + html/template, 쿠키 인증) 마운트: /admin
	webServer.Register(router.Mux())

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

// ensureSuperAdmin 은 super_admin 이 하나도 없으면 항상(모든 환경) 생성한다.
// 비밀번호: env(APP_AUTH_SEED_ADMIN_PASSWORD) 우선, 없으면 랜덤 생성 후 1회 경고 로그.
// 실패는 로깅만 하고 기동을 막지 않는다.
func ensureSuperAdmin(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	members domainauth.Repository,
	hasher domainauth.PasswordHasher,
) {
	if _, total, err := members.List(ctx, domainauth.MemberFilter{RoleID: domainauth.RoleIDSuperAdmin}); err != nil {
		log.Error("ensure super admin: list failed", "error", err)
		return
	} else if total > 0 {
		return // 이미 존재 → 아무 것도 하지 않음
	}

	username := cfg.Auth.SeedAdminUsername
	if username == "" {
		username = defaultSeedAdminUsername
	}
	password := cfg.Auth.SeedAdminPassword
	generated := false
	if password == "" {
		pw, err := randomPassword(16)
		if err != nil {
			log.Error("ensure super admin: generate password failed", "error", err)
			return
		}
		password, generated = pw, true
	}

	hash, err := hasher.Hash(password)
	if err != nil {
		log.Error("ensure super admin: hash failed", "error", err)
		return
	}
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
		log.Error("ensure super admin: save failed", "error", err)
		return
	}
	if generated {
		// 보안: 생성된 비밀번호는 이 로그에 1회만 노출된다. 로그인 후 즉시 변경할 것.
		log.Warn("super admin created with GENERATED password — change it immediately after login",
			"username", username, "password", password)
	} else {
		log.Info("super admin created", "username", username)
	}
}

// randomPassword 는 URL-safe 랜덤 비밀번호를 생성한다.
func randomPassword(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// seedSampleProvider 는 비운영 환경에서 활성 Provider 가 없으면 샘플(local-ollama)을 생성한다.
func seedSampleProvider(ctx context.Context, log *slog.Logger, providers domainllmprovider.Repository) {
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
