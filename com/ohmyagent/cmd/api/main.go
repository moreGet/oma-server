// Command api 는 OhMyAgent AI Agent 서버의 조립 루트(DI)다(스펙 §3.7).
// 비즈니스 로직 금지: config→어댑터→유스케이스→핸들러→라우트→미들웨어→graceful shutdown 만 수행.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	httpin "aiagent/com/ohmyagent/internal/adapter/in/http"
	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	"aiagent/com/ohmyagent/internal/adapter/in/web"
	authout "aiagent/com/ohmyagent/internal/adapter/out/auth"
	cryptoout "aiagent/com/ohmyagent/internal/adapter/out/crypto"
	dbout "aiagent/com/ohmyagent/internal/adapter/out/db"
	llmout "aiagent/com/ohmyagent/internal/adapter/out/llm"
	messagingbus "aiagent/com/ohmyagent/internal/adapter/out/messagingbus"
	sessionstore "aiagent/com/ohmyagent/internal/adapter/out/sessionstore"
	transcriptout "aiagent/com/ohmyagent/internal/adapter/out/transcript"
	agentapp "aiagent/com/ohmyagent/internal/application/agent"
	agentregistryapp "aiagent/com/ohmyagent/internal/application/agentregistry"
	authapp "aiagent/com/ohmyagent/internal/application/auth"
	chatapp "aiagent/com/ohmyagent/internal/application/chat"
	chatsessionapp "aiagent/com/ohmyagent/internal/application/chatsession"
	clientversionapp "aiagent/com/ohmyagent/internal/application/clientversion"
	llmproviderapp "aiagent/com/ohmyagent/internal/application/llmprovider"
	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	projectapp "aiagent/com/ohmyagent/internal/application/project"
	quotaapp "aiagent/com/ohmyagent/internal/application/quota"
	serviceaccountapp "aiagent/com/ohmyagent/internal/application/serviceaccount"
	sessionapp "aiagent/com/ohmyagent/internal/application/session"
	toolpolicyapp "aiagent/com/ohmyagent/internal/application/toolpolicy"
	transcriptapp "aiagent/com/ohmyagent/internal/application/transcript"
	"aiagent/com/ohmyagent/internal/config"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	"aiagent/com/ohmyagent/internal/logger"
)

const (
	// shutdownTimeout: graceful shutdown 시 진행 중 요청 대기 한도.
	shutdownTimeout = 15 * time.Second
	// migrationTimeout: 기동 시 DB 마이그레이션(Up/Reset) 작업 한도.
	migrationTimeout = 30 * time.Second
	// seedTimeout: 비운영 시딩 작업 한도.
	seedTimeout = 10 * time.Second
	// seedPasswordBytes: 시드 admin 랜덤 비밀번호 바이트 수.
	seedPasswordBytes = 16
	// defaultSeedAdminUsername: 시드 admin 사용자명 미설정 시 기본값.
	defaultSeedAdminUsername = "admin"
	// transcriptBufferSize: 대화 이력 비동기 기록 버퍼. 초과분은 드롭(백프레셔).
	transcriptBufferSize = 4096
	// transcriptPurgeInterval: 대화 이력 보존 정책(TTL) purge 주기.
	transcriptPurgeInterval = 6 * time.Hour
	// idleTimeout: keep-alive 유휴 커넥션 한도(고동접에서 커넥션 재활용·정리).
	idleTimeout = 120 * time.Second
	// readHeaderTimeout: 요청 헤더 수신 한도(slowloris 완화).
	readHeaderTimeout = 10 * time.Second
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
	// 비운영(local/dev)은 DEBUG 레벨 + 호출 위치(source)로 상세 로깅, 운영은 INFO.
	logOpts := logger.Options{Level: "info", Format: "json"}
	switch cfg.Env {
	case "local", "dev", "development":
		logOpts.Level, logOpts.Source = "debug", true
	}
	log := logger.Init(logOpts)
	defer logger.Shutdown() // 종료 시 비동기 로그 버퍼 플러시(가장 마지막에 실행)
	log.Info("starting server", "env", cfg.Env, "addr", cfg.ServerAddr())

	// 2) DB + 마이그레이션
	conn, err := dbout.Open(cfg.Database.Driver, cfg.Database.DSN, cfg.Database.MaxOpenConns)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	migCtx, migCancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer migCancel()
	if cfg.ResetsDatabase() {
		// APP_DB_RESET 옵트인: DB 를 drop & create 하여 깨끗한 스키마 + 시드 재생성(모든 데이터 삭제 주의).
		log.Warn("APP_DB_RESET enabled: resetting database — DROPS ALL DATA, then re-seeds")
		if err := dbout.ResetMigrations(migCtx, cfg.Database.Driver, conn); err != nil {
			return err
		}
	} else if err := dbout.RunMigrations(migCtx, cfg.Database.Driver, conn); err != nil {
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
	providerCipher := cryptoout.NewAESGCMCipher(cfg.Security.EncryptionSecret) // API 키/대화이력 시크릿 암복호화(AES-GCM)
	// 대화 이력 저장: DB gzip BLOB 기본 + 어드민 선택형 백엔드(file/S3). recorder 는 아래(authUC 이후)에서 생성.
	transcriptDBStore := dbout.NewTranscriptStore(conn)
	transcriptSettingsRepo := dbout.NewTranscriptSettingsRepository(conn)
	transcriptFactory := transcriptout.NewStoreFactory(transcriptDBStore, providerCipher)

	// 4) 유스케이스(auth → provider 에 accessGate 주입)
	authUC := authapp.NewAuthUseCase(memberRepo, roleRepo, hasher, tokenSvc)
	// 대화 이력 매니저(설정 로드 + 활성 백엔드 구성) + 비차단 recorder(꺼져 있으면 기록 생략).
	transcriptManager, err := transcriptapp.NewManager(transcriptSettingsRepo, transcriptFactory, providerCipher, authUC)
	if err != nil {
		return err
	}
	transcriptRecorder := transcriptout.NewAsyncRecorder(transcriptManager, transcriptManager.Enabled, transcriptBufferSize)
	defer transcriptRecorder.Close() // 종료 시 잔여 이력 플러시
	// 토큰 쿼터(월간 사용 한도). 카운터/한도 모두 DB → 다중 인스턴스(LB) 정확.
	quotaService := quotaapp.NewService(dbout.NewQuotaRepository(conn, cfg.Database.Driver), authUC)
	// 도구 정책(노출/실행 + 위험명령 차단). 전역 단일 행 + 멤버 오버라이드, atomic 캐시 → 어드민 편집 즉시 반영.
	toolPolicyManager, err := toolpolicyapp.NewManager(dbout.NewToolPolicyRepository(conn), dbout.NewMemberToolPolicyRepository(conn), authUC)
	if err != nil {
		return err
	}
	// 프로젝트/대화 동기화: 메타데이터 DB + 본문 선택형 백엔드(DB/파일/S3) + 계정별 세션 캡.
	sessionFactory := sessionstore.NewStoreFactory(dbout.NewSessionBlobStore(conn), providerCipher)
	sessionManager, err := sessionapp.NewManager(dbout.NewSessionSettingsRepository(conn), dbout.NewMemberSessionLimitRepository(conn), sessionFactory, providerCipher, authUC)
	if err != nil {
		return err
	}
	projectService := projectapp.NewService(dbout.NewProjectRepository(conn, cfg.Database.Driver), dbout.NewConversationRepository(conn, cfg.Database.Driver), sessionManager, sessionManager.EffectiveMaxSessions)
	// 사용자 간 실시간 채팅(단체/1:1): RDB 영속 + 인메모리 브로드캐스트 허브(단일 인스턴스).
	messagingHub := messagingapp.NewHub()
	messagingBroadcaster, err := buildBroadcaster(cfg, messagingHub, log)
	if err != nil {
		return err
	}
	defer func() { _ = messagingBroadcaster.Close() }()
	messagingRepo := dbout.NewMessagingRepository(conn, cfg.Database.Driver)
	messagingService := messagingapp.NewService(messagingRepo, messagingRepo, dbout.NewChatAttachmentStore(conn), messagingHub, messagingBroadcaster)
	messagingService.SetMemberDirectory(dbout.NewMemberDirectoryRepository(conn)) // 채팅 멤버 이름 해석(UUID→username/display_name)
	// 에이전트 레지스트리(등록·발견·생존성) + A2A 토큰 브로커(ES256 서명 키 bootstrap — 없으면 생성·암호화 저장).
	registryUC := agentregistryapp.NewService(dbout.NewAgentRegistryRepository(conn, cfg.Database.Driver), authUC,
		cfg.Registry.HeartbeatInterval.Std(), cfg.Registry.LeaseTTL.Std())
	registryUC.SetMemberDirectory(dbout.NewMemberDirectoryRepository(conn)) // 어드민 owner 이름 표시
	if err := registryUC.EnableBroker(context.Background(), dbout.NewA2AKeyRepository(conn), cryptoout.NewES256Signer(), providerCipher, cfg.Registry.TokenTTL.Std()); err != nil {
		return err
	}
	providerUC := llmproviderapp.NewProviderService(providerRepo, providerCache, providerFactory, providerCipher, authUC)
	chatUC := chatapp.NewChatService(providerUC) // providerUC 가 활성 어댑터 resolver 를 충족
	// 에이전트 루프(tools/function-calling) 중계. 도구 정책은 요청 게이트로 강제한다
	// (차단 도구가 실리면 403 — 모델에 스키마 자체를 넘기지 않는다).
	agentUC := agentapp.NewAgentService(providerUC, toolPolicyManager)
	sessionUC := chatsessionapp.NewSessionService(sessionRepo)
	// 서비스 계정 + 장수 API 키: 관리 유스케이스(admin) + oma_sa_ API키 인증기(Secured 라우터에 주입).
	// authUC 를 accessGate 로 재사용(admin 인가 + owner 실재 검증). 토큰은 SHA-256 해시만 저장(평문 미보관).
	serviceAccountUC := serviceaccountapp.NewService(dbout.NewServiceAccountRepository(conn), cryptoout.NewSATokenHasher(), authUC)

	// 5) 시딩: super_admin 은 모든 환경에서 항상 보장(없으면 생성). 기본 Provider 는 비운영만.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), seedTimeout)
	ensureSuperAdmin(seedCtx, log, cfg, memberRepo, hasher)
	if cfg.SeedsInitialAdmin() {
		seedDefaultProviders(seedCtx, log, providerRepo)
	}
	seedCancel()

	// 6) 핸들러
	clientVersionManager, err := clientversionapp.NewManager(dbout.NewClientVersionRepository(conn), authUC)
	if err != nil {
		return err
	}
	h := apiHandlers{
		auth:             httpin.NewAuthHandler(authUC),
		provider:         httpin.NewProviderHandler(providerUC),
		chat:             httpin.NewChatHandler(chatUC, transcriptRecorder, quotaService),
		agent:            httpin.NewAgentHandler(agentUC, transcriptRecorder, transcriptManager.StripAttachments, quotaService),
		health:           httpin.NewHealthHandler(conn),
		models:           httpin.NewModelsHandler(providerUC),
		suggestion:       httpin.NewSuggestionHandler(),
		session:          httpin.NewSessionHandler(sessionUC),
		stats:            httpin.NewStatsHandler(authUC, providerUC),
		quota:            httpin.NewQuotaHandler(quotaService),
		project:          httpin.NewProjectHandler(projectService),
		messaging:        httpin.NewMessagingHandler(messagingService),
		chatWS:           httpin.NewChatWSHandler(messagingService),
		client:           httpin.NewClientHandler(clientVersionManager, toolPolicyManager),
		memberToolPolicy: httpin.NewMemberToolPolicyHandler(toolPolicyManager),
		agentReg:         httpin.NewAgentRegistryHandler(registryUC),
		serviceAccount:   httpin.NewServiceAccountHandler(serviceAccountUC),
	}
	webServer := web.NewServer(authUC, providerUC, transcriptManager, quotaService, sessionManager, toolPolicyManager, clientVersionManager, messagingService, registryUC, tokenSvc, cfg.Auth.JWTExpiry.Std(), cfg.Env == "prod")

	// 7) 라우트 등록(설계 §7). serviceAccountUC 를 API키 인증기로 Secured 라우터에 주입한다.
	router := registerRoutes(tokenSvc, serviceAccountUC, h)

	// 어드민 웹 페이지(htmx + html/template, 쿠키 인증) 마운트: /admin
	webServer.Register(router.Mux())

	// 8) 미들웨어 체인 + 서버
	handler := security.Chain(router.Mux(), cfg.Security.AllowedOrigins)
	srv := &http.Server{
		Addr:              cfg.ServerAddr(),
		Handler:           handler,
		ReadTimeout:       cfg.Server.ReadTimeout.Std(),
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout.Std(),
		IdleTimeout:       idleTimeout,
	}

	// 9) graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 대화 이력 보존(TTL) purge 백그라운드 잡(종료 시 ctx 로 정리).
	go transcriptManager.RunPurge(ctx, transcriptPurgeInterval)
	// 에이전트 레지스트리 sweeper: offline 로 오래 방치된 레코드 정리(sweep_interval=0 이면 내부에서 즉시 종료).
	go registryUC.RunSweeper(ctx, cfg.Registry.SweepInterval.Std())

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

// buildBroadcaster 는 설정에 따라 채팅 이벤트 전파 방식을 만든다.
// memory(기본)=단일 인스턴스 로컬 허브, redis=다중 인스턴스 pub/sub(접속 실패 시 기동 중단).
func buildBroadcaster(cfg *config.Config, hub *messagingapp.Hub, log *slog.Logger) (messagingapp.Broadcaster, error) {
	if !cfg.UsesRedisBroadcaster() {
		log.Info("chat broadcaster: memory (single instance)")
		return messagingapp.NewLocalBroadcaster(hub), nil
	}
	addr := cfg.Messaging.Redis.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	channel := cfg.Messaging.Redis.Channel
	if channel == "" {
		channel = "ohmyagent:chat"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bc, err := messagingbus.NewRedisBroadcaster(ctx,
		&redis.Options{
			Addr: addr, Password: cfg.Messaging.Redis.Password, DB: cfg.Messaging.Redis.DB,
			DialTimeout: 3 * time.Second, MaxRetries: 1, // 기동 시 빠른 실패
		},
		channel, hub.SendToMembers)
	if err != nil {
		return nil, fmt.Errorf("messaging redis broadcaster: %w", err)
	}
	log.Info("chat broadcaster: redis (multi-instance)", "addr", addr, "channel", channel)
	return bc, nil
}

// ensureSuperAdmin 은 super_admin 이 없으면 항상(모든 환경) 생성한다(비밀번호는 env 우선, 없으면 랜덤 생성 후 1회 경고).
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
		pw, err := randomPassword(seedPasswordBytes)
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

// defaultProviders 는 비운영 환경에서 항상 보장하는 기본 Provider 목록이다.
// **목록의 첫 항목이 기본 활성 Provider 다**(활성이 하나도 없을 때 적용).
//
// API 키는 **환경변수명만** 담는다(APIKeyEnv). 키 원문을 여기에 적으면 시크릿이 git 이력에
// 영구히 남는다 — 나중에 지워도 이력에는 남으므로 되돌릴 수 없다(TEMPLATE-SPEC §10).
// 실제 키는 프로세스 환경에 둔다(예: 리포 루트 `.env` — .gitignore 대상).
var defaultProviders = []domainllmprovider.LLMProvider{
	{
		Name:         "openai-gpt-5.6-luna",
		ProviderType: domainllmprovider.ProviderTypeExternal,
		Config: domainllmprovider.ProviderConfig{
			Model:     "gpt-5.6-luna",
			APIKeyEnv: "OPENAI_API_KEY",
		},
	},
	{
		Name:         "local-ollama",
		ProviderType: domainllmprovider.ProviderTypeLocal,
		Config:       domainllmprovider.ProviderConfig{Endpoint: "http://localhost:11434", Model: "llama3"},
	},
}

// seedDefaultProviders 는 기본 Provider 를 **이름 기준으로 없을 때만** 생성한다.
//
// 이름 기준 idempotent 이므로 재시작·DB 재생성 후에도 기본값이 되살아나고, 이미 있으면
// 건드리지 않아 어드민이 바꾼 설정(모델·키·활성 여부)을 덮어쓰지 않는다.
// 활성 Provider 가 하나도 없을 때만 첫 항목을 활성으로 만든다(활성은 항상 1개).
func seedDefaultProviders(ctx context.Context, log *slog.Logger, providers domainllmprovider.Repository) {
	existing, err := providers.List(ctx)
	if err != nil {
		log.Error("seed provider: list failed", "error", err)
		return
	}
	byName := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		byName[p.Name] = struct{}{}
	}
	_, activeErr := providers.GetActive(ctx)
	needActive := errors.Is(activeErr, domainllmprovider.ErrNoActiveProvider)

	now := time.Now().UTC().Truncate(time.Second)
	for _, d := range defaultProviders {
		if _, ok := byName[d.Name]; ok {
			continue
		}
		p := d // 루프 변수 복사(ID/시각을 채워 저장)
		p.ID = uuid.NewString()
		p.CreatedAt, p.UpdatedAt = now, now
		if needActive {
			p.IsActive = true
			needActive = false
		}
		if err := providers.Save(ctx, p); err != nil {
			log.Error("seed provider: save failed", "name", p.Name, "error", err)
			continue
		}
		log.Info("seed provider created", "name", p.Name, "model", p.Config.Model, "active", p.IsActive)
	}
}

// apiHandlers 는 라우트 등록에 필요한 HTTP 핸들러 묶음이다(run 의 #6 조립 결과).
type apiHandlers struct {
	auth             *httpin.AuthHandler
	provider         *httpin.ProviderHandler
	chat             *httpin.ChatHandler
	agent            *httpin.AgentHandler
	health           *httpin.HealthHandler
	models           *httpin.ModelsHandler
	suggestion       *httpin.SuggestionHandler
	session          *httpin.SessionHandler
	stats            *httpin.StatsHandler
	quota            *httpin.QuotaHandler
	project          *httpin.ProjectHandler
	messaging        *httpin.MessagingHandler
	chatWS           *httpin.ChatWSHandler
	client           *httpin.ClientHandler
	memberToolPolicy *httpin.MemberToolPolicyHandler
	agentReg         *httpin.AgentRegistryHandler
	serviceAccount   *httpin.ServiceAccountHandler
}

// registerRoutes 는 API 라우트를 등록한 SecureRouter 를 만든다(설계 §7 — run 의 #7 구획 분리).
// apiKeyAuth 는 서비스 계정 oma_sa_ API 키 인증기다(Secured 라우터가 접두사로 JWT/API키 경로를 분기).
func registerRoutes(tokenSvc domainauth.TokenService, apiKeyAuth security.APIKeyAuthenticator, h apiHandlers) *security.SecureRouter {
	router := security.NewSecureRouter(http.NewServeMux(), tokenSvc, security.WithAPIKeyAuth(apiKeyAuth))

	// 도메인별 등록. ServeMux(Go 1.22+)는 등록 순서가 아니라 패턴 구체성으로 매칭하므로
	// 그룹 순서는 동작에 영향을 주지 않는다(리터럴 세그먼트가 {와일드카드}보다 항상 우선).
	registerAuthRoutes(router, h)
	registerProviderRoutes(router, h)
	registerAgentClientRoutes(router, h)
	registerMessagingRoutes(router, h)
	registerAgentRegistryRoutes(router, h)
	registerServiceAccountRoutes(router, h)
	return router
}

// registerAuthRoutes 는 로그인·본인 정보·멤버 관리·대시보드 집계를 등록한다(평면 envelope).
func registerAuthRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Public("POST /api/v1/auth/login", httpin.Handle(h.auth.Login))

	// 본인 정보·비밀번호·역할목록(인증된 사용자)
	router.Secured("GET /api/v1/me", httpin.Handle(h.auth.Me), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/me/password", httpin.Handle(h.auth.ChangePassword), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/me/quota", httpin.Handle(h.quota.Me), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/users/me", httpin.HandleAgent(h.auth.Profile), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/roles", httpin.Handle(h.auth.ListRoles), security.MinRole(domainauth.RoleLevelUser))

	router.Secured("GET /api/v1/members", httpin.Handle(h.auth.ListMembers), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/members", httpin.Handle(h.auth.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}", httpin.Handle(h.auth.GetMember), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/members/{id}/role", httpin.Handle(h.auth.ChangeRole), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/active", httpin.Handle(h.auth.SetActive), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/members/{id}", httpin.Handle(h.auth.DeleteMember), security.MinRole(domainauth.RoleLevelSuperAdmin))
	router.Secured("PUT /api/v1/members/{id}/password", httpin.Handle(h.auth.ResetPassword), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}/tool-policy", httpin.Handle(h.memberToolPolicy.Get), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/tool-policy", httpin.Handle(h.memberToolPolicy.Put), security.MinRole(domainauth.RoleLevelAdmin))

	// 대시보드 집계(admin↑)
	router.Secured("GET /api/v1/statistics", httpin.Handle(h.stats.Get), security.MinRole(domainauth.RoleLevelAdmin))
}

// registerProviderRoutes 는 LLM Provider 관리와 질의(SSE)를 등록한다(평면 envelope).
func registerProviderRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Secured("GET /api/v1/llm-providers", httpin.Handle(h.provider.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/llm-providers/{id}", httpin.Handle(h.provider.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/llm-providers", httpin.Handle(h.provider.Create), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PATCH /api/v1/llm-providers/{id}/config", httpin.Handle(h.provider.UpdateConfig), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/llm-providers/{id}/activate", httpin.Handle(h.provider.Activate), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/llm-providers/{id}", httpin.Handle(h.provider.Delete), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/llm-providers/{id}/test", httpin.Handle(h.provider.Test), security.MinRole(domainauth.RoleLevelAdmin))

	// 질의(클라이언트 → 활성 LLM → SSE 응답). 인증된 사용자(user↑) 누구나.
	router.Secured("POST /api/v1/chat", httpin.Handle(h.chat.Stream), security.MinRole(domainauth.RoleLevelUser))
}

// registerAgentClientRoutes 는 C# 에이전트 클라이언트 계약(API_CONTRACT)을 등록한다.
// 에러 envelope 은 클라이언트 계약대로 { "error": { code, message } } (HandleAgent).
func registerAgentClientRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Public("GET /api/v1/health", httpin.HandleAgent(h.health.Check)) // 헬스/연결 체크(인증 불필요)

	router.Secured("GET /api/v1/models", httpin.HandleAgent(h.models.List), security.MinRole(domainauth.RoleLevelUser))

	// 에이전트 루프의 심장: 대화기록 + 도구스키마 → SSE(텍스트/도구호출/stop_reason).
	router.Secured("POST /api/v1/agent/chat", httpin.HandleAgent(h.agent.Chat), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agent/suggestions", httpin.HandleAgent(h.suggestion.List), security.MinRole(domainauth.RoleLevelUser))

	// 클라이언트 계약: 도구 정책 게이트 + 버전 점검(둘 다 선택 기능, graceful).
	router.Secured("GET /api/v1/tools/policy", httpin.HandleAgent(h.client.ToolsPolicy), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/tools/authorize", httpin.HandleAgent(h.client.ToolsAuthorize), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/client/version", httpin.HandleAgent(h.client.ClientVersion), security.MinRole(domainauth.RoleLevelUser))
	// 서버 제어형 위험명령/경로 차단 패턴(클라 디폴트에 추가만 — 2중 안전). 미설정 시 빈 목록.
	router.Secured("GET /api/v1/security/command-policy", httpin.HandleAgent(h.client.CommandPolicy), security.MinRole(domainauth.RoleLevelUser))

	// 채팅 히스토리 서버 동기화(소유권 스코프).
	router.Secured("GET /api/v1/agent/sessions", httpin.HandleAgent(h.session.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agent/sessions/{id}", httpin.HandleAgent(h.session.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/agent/sessions/{id}", httpin.HandleAgent(h.session.Upsert), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/agent/sessions/{id}", httpin.HandleAgent(h.session.Delete), security.MinRole(domainauth.RoleLevelUser))

	// 프로젝트/대화 동기화(클라 로컬 우선 → 서버 동기화, 소유권 스코프, 중첩 envelope).
	router.Secured("GET /api/v1/projects", httpin.HandleAgent(h.project.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/projects", httpin.HandleAgent(h.project.Upsert), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/projects/{id}", httpin.HandleAgent(h.project.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/projects/{id}", httpin.HandleAgent(h.project.Delete), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/projects/{id}/conversations", httpin.HandleAgent(h.project.UpsertConversation), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/projects/{id}/conversations/{cid}", httpin.HandleAgent(h.project.DeleteConversation), security.MinRole(domainauth.RoleLevelUser))
}

// registerMessagingRoutes 는 사용자 간 실시간 채팅(단체/1:1)을 등록한다.
// WS 는 Bearer 헤더로 인증. REST 는 방/이력/첨부 관리(중첩 envelope).
func registerMessagingRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Secured("GET /api/v1/chat/ws", httpin.HandleAgent(h.chatWS.Serve), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms", httpin.HandleAgent(h.messaging.ListRooms), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms", httpin.HandleAgent(h.messaging.CreateGroup), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/direct", httpin.HandleAgent(h.messaging.CreateDirect), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/messages", httpin.HandleAgent(h.messaging.History), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/messages", httpin.HandleAgent(h.messaging.SendMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PATCH /api/v1/chat/rooms/{id}/messages/{mid}", httpin.HandleAgent(h.messaging.EditMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/chat/rooms/{id}/messages/{mid}", httpin.HandleAgent(h.messaging.DeleteMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/read", httpin.HandleAgent(h.messaging.MarkRead), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/reads", httpin.HandleAgent(h.messaging.ReadStates), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/unread", httpin.HandleAgent(h.messaging.Unread), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/members", httpin.HandleAgent(h.messaging.RoomMembers), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/members", httpin.HandleAgent(h.messaging.AddMembers), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/chat/rooms/{id}/members/{mid}", httpin.HandleAgent(h.messaging.Kick), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/leave", httpin.HandleAgent(h.messaging.Leave), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/presence", httpin.HandleAgent(h.messaging.Presence), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/mentions", httpin.HandleAgent(h.messaging.Mentions), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/attachments", httpin.HandleAgent(h.messaging.UploadAttachment), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/attachments/{aid}", httpin.HandleAgent(h.messaging.DownloadAttachment), security.MinRole(domainauth.RoleLevelUser))
}

// registerAgentRegistryRoutes 는 에이전트 레지스트리(§공유 계약)를 등록한다:
// 등록·heartbeat·해제·발견 + A2A 토큰 브로커(평면 envelope).
// 주의: 리터럴 a2a-public-key 가 {id} 보다 우선 매칭된다(Go 1.22+ ServeMux 구체 경로 우선).
func registerAgentRegistryRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Secured("POST /api/v1/agents/register", httpin.Handle(h.agentReg.Register), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/agents/{id}/heartbeat", httpin.Handle(h.agentReg.Heartbeat), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/agents/{id}", httpin.Handle(h.agentReg.Deregister), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agents", httpin.Handle(h.agentReg.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agents/{id}", httpin.Handle(h.agentReg.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/agents/{id}/token", httpin.Handle(h.agentReg.MintToken), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agents/a2a-public-key", httpin.Handle(h.agentReg.PublicKey), security.MinRole(domainauth.RoleLevelUser))
}

// registerServiceAccountRoutes 는 서비스 계정 + 장수 API 키 관리를 등록한다
// (스펙 §2D — 전부 admin 전용, 유스케이스 RequireAdmin 이중 게이트).
// 리터럴 keys 세그먼트가 {id} 와일드카드보다 우선 매칭된다(Go 1.22+ ServeMux).
func registerServiceAccountRoutes(router *security.SecureRouter, h apiHandlers) {
	router.Secured("POST /api/v1/service-accounts", httpin.Handle(h.serviceAccount.Create), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/service-accounts", httpin.Handle(h.serviceAccount.List), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/service-accounts/{id}", httpin.Handle(h.serviceAccount.Delete), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/service-accounts/{id}/keys", httpin.Handle(h.serviceAccount.IssueKey), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/service-accounts/{id}/keys", httpin.Handle(h.serviceAccount.ListKeys), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/service-accounts/{id}/keys/{key_id}", httpin.Handle(h.serviceAccount.RevokeKey), security.MinRole(domainauth.RoleLevelAdmin))
}
