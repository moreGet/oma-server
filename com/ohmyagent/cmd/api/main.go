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
	authapp "aiagent/com/ohmyagent/internal/application/auth"
	chatapp "aiagent/com/ohmyagent/internal/application/chat"
	chatsessionapp "aiagent/com/ohmyagent/internal/application/chatsession"
	clientversionapp "aiagent/com/ohmyagent/internal/application/clientversion"
	llmproviderapp "aiagent/com/ohmyagent/internal/application/llmprovider"
	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	projectapp "aiagent/com/ohmyagent/internal/application/project"
	quotaapp "aiagent/com/ohmyagent/internal/application/quota"
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
	providerUC := llmproviderapp.NewProviderService(providerRepo, providerCache, providerFactory, providerCipher, authUC)
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
	chatH := httpin.NewChatHandler(chatUC, transcriptRecorder, quotaService)
	agentH := httpin.NewAgentHandler(agentUC, transcriptRecorder, transcriptManager.StripAttachments, quotaService)
	healthH := httpin.NewHealthHandler(conn)
	modelsH := httpin.NewModelsHandler(providerUC)
	suggestionH := httpin.NewSuggestionHandler()
	sessionH := httpin.NewSessionHandler(sessionUC)
	statsH := httpin.NewStatsHandler(authUC, providerUC)
	quotaH := httpin.NewQuotaHandler(quotaService)
	projectH := httpin.NewProjectHandler(projectService)
	messagingH := httpin.NewMessagingHandler(messagingService)
	chatWSH := httpin.NewChatWSHandler(messagingService)
	clientVersionManager, err := clientversionapp.NewManager(dbout.NewClientVersionRepository(conn), authUC)
	if err != nil {
		return err
	}
	clientH := httpin.NewClientHandler(clientVersionManager, toolPolicyManager)
	memberToolPolicyH := httpin.NewMemberToolPolicyHandler(toolPolicyManager)
	webServer := web.NewServer(authUC, providerUC, transcriptManager, quotaService, sessionManager, toolPolicyManager, clientVersionManager, messagingService, tokenSvc, cfg.Auth.JWTExpiry.Std(), cfg.Env == "prod")

	// 7) 라우트 등록(설계 §7)
	router := security.NewSecureRouter(http.NewServeMux(), tokenSvc)

	router.Public("POST /api/v1/auth/login", httpin.Handle(authH.Login))

	// 본인 정보·비밀번호·역할목록(인증된 사용자)
	router.Secured("GET /api/v1/me", httpin.Handle(authH.Me), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/me/password", httpin.Handle(authH.ChangePassword), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/me/quota", httpin.Handle(quotaH.Me), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/users/me", httpin.HandleAgent(authH.Profile), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/roles", httpin.Handle(authH.ListRoles), security.MinRole(domainauth.RoleLevelUser))

	router.Secured("GET /api/v1/members", httpin.Handle(authH.ListMembers), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("POST /api/v1/members", httpin.Handle(authH.CreateMember), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}", httpin.Handle(authH.GetMember), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/members/{id}/role", httpin.Handle(authH.ChangeRole), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/active", httpin.Handle(authH.SetActive), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("DELETE /api/v1/members/{id}", httpin.Handle(authH.DeleteMember), security.MinRole(domainauth.RoleLevelSuperAdmin))
	router.Secured("PUT /api/v1/members/{id}/password", httpin.Handle(authH.ResetPassword), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("GET /api/v1/members/{id}/tool-policy", httpin.Handle(memberToolPolicyH.Get), security.MinRole(domainauth.RoleLevelAdmin))
	router.Secured("PUT /api/v1/members/{id}/tool-policy", httpin.Handle(memberToolPolicyH.Put), security.MinRole(domainauth.RoleLevelAdmin))

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

	// 클라이언트 계약: 도구 정책 게이트 + 버전 점검(둘 다 선택 기능, graceful).
	router.Secured("GET /api/v1/tools/policy", httpin.HandleAgent(clientH.ToolsPolicy), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/tools/authorize", httpin.HandleAgent(clientH.ToolsAuthorize), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/client/version", httpin.HandleAgent(clientH.ClientVersion), security.MinRole(domainauth.RoleLevelUser))
	// 서버 제어형 위험명령/경로 차단 패턴(클라 디폴트에 추가만 — 2중 안전). 미설정 시 빈 목록.
	router.Secured("GET /api/v1/security/command-policy", httpin.HandleAgent(clientH.CommandPolicy), security.MinRole(domainauth.RoleLevelUser))

	// 채팅 히스토리 서버 동기화(소유권 스코프).
	router.Secured("GET /api/v1/agent/sessions", httpin.HandleAgent(sessionH.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PUT /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Upsert), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/agent/sessions/{id}", httpin.HandleAgent(sessionH.Delete), security.MinRole(domainauth.RoleLevelUser))

	// 프로젝트/대화 동기화(클라 로컬 우선 → 서버 동기화, 소유권 스코프, 중첩 envelope).
	router.Secured("GET /api/v1/projects", httpin.HandleAgent(projectH.List), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/projects", httpin.HandleAgent(projectH.Upsert), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/projects/{id}", httpin.HandleAgent(projectH.Get), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/projects/{id}", httpin.HandleAgent(projectH.Delete), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/projects/{id}/conversations", httpin.HandleAgent(projectH.UpsertConversation), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/projects/{id}/conversations/{cid}", httpin.HandleAgent(projectH.DeleteConversation), security.MinRole(domainauth.RoleLevelUser))

	// 사용자 간 실시간 채팅(단체/1:1). WS 는 Bearer 헤더로 인증. REST 는 방/이력 관리.
	router.Secured("GET /api/v1/chat/ws", httpin.HandleAgent(chatWSH.Serve), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms", httpin.HandleAgent(messagingH.ListRooms), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms", httpin.HandleAgent(messagingH.CreateGroup), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/direct", httpin.HandleAgent(messagingH.CreateDirect), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/messages", httpin.HandleAgent(messagingH.History), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/messages", httpin.HandleAgent(messagingH.SendMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("PATCH /api/v1/chat/rooms/{id}/messages/{mid}", httpin.HandleAgent(messagingH.EditMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/chat/rooms/{id}/messages/{mid}", httpin.HandleAgent(messagingH.DeleteMessage), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/read", httpin.HandleAgent(messagingH.MarkRead), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/reads", httpin.HandleAgent(messagingH.ReadStates), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/unread", httpin.HandleAgent(messagingH.Unread), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/members", httpin.HandleAgent(messagingH.RoomMembers), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/members", httpin.HandleAgent(messagingH.AddMembers), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("DELETE /api/v1/chat/rooms/{id}/members/{mid}", httpin.HandleAgent(messagingH.Kick), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/rooms/{id}/leave", httpin.HandleAgent(messagingH.Leave), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/rooms/{id}/presence", httpin.HandleAgent(messagingH.Presence), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/mentions", httpin.HandleAgent(messagingH.Mentions), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("POST /api/v1/chat/attachments", httpin.HandleAgent(messagingH.UploadAttachment), security.MinRole(domainauth.RoleLevelUser))
	router.Secured("GET /api/v1/chat/attachments/{aid}", httpin.HandleAgent(messagingH.DownloadAttachment), security.MinRole(domainauth.RoleLevelUser))

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
