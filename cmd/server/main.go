// Command server 는 LLM 제공자 동적 스위칭 시스템의 HTTP API 서버를 기동한다.
//
// 의존성 조립 순서:
//  1. config.Load
//  2. infrastructure.NewMariaDB
//  3. db.NewLLMProviderRepository (sqlc Queries 내부 보유)
//  4. cache.NewProviderCache
//  5. llm.NewLLMFactory
//  6. application.NewLLMProviderService(repo, cache, factory)
//  7. handler.NewLLMProviderHandler(svc)
//  8. router.New(handler, logger)
//  9. r.Run(cfg.ServerAddr())
package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/adapter/cache"
	dbadapter "OhMyAgent.AiAgent.Server/internal/adapter/db"
	"OhMyAgent.AiAgent.Server/internal/adapter/llm"
	"OhMyAgent.AiAgent.Server/internal/application"
	"OhMyAgent.AiAgent.Server/internal/config"
	"OhMyAgent.AiAgent.Server/internal/handler"
	"OhMyAgent.AiAgent.Server/internal/infrastructure"
	"OhMyAgent.AiAgent.Server/internal/router"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer func() { _ = logger.Sync() }()

	// 1. config
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("load config", zap.Error(err))
	}
	if cfg.Server.Mode != "" {
		gin.SetMode(cfg.Server.Mode)
	}

	// YAML에 명시된 API 키를 환경변수로 노출 (LLM 어댑터가 os.Getenv로 읽음)
	if cfg.LLM.AnthropicAPIKey != "" {
		os.Setenv("ANTHROPIC_API_KEY", cfg.LLM.AnthropicAPIKey)
	}
	if cfg.LLM.OpenAIAPIKey != "" {
		os.Setenv("OPENAI_API_KEY", cfg.LLM.OpenAIAPIKey)
	}

	// 2. DB 연결
	sqlDB, err := infrastructure.NewMariaDB(cfg.Database)
	if err != nil {
		logger.Fatal("connect mariadb", zap.Error(err))
	}
	defer func() { _ = sqlDB.Close() }()

	// 3. DB 어댑터 (port.LLMRepository 구현)
	repo := dbadapter.NewLLMProviderRepository(sqlDB)

	// 4. 캐시 (port.ProviderCache 구현)
	providerCache := cache.NewProviderCache()

	// 5. LLM 팩토리 (port.LLMFactory 구현)
	factory := llm.NewLLMFactory()

	// 6. Application 서비스
	svc := application.NewLLMProviderService(repo, providerCache, factory)

	// 7. HTTP 핸들러
	llmHandler := handler.NewLLMProviderHandler(svc)

	// 8. 라우터
	r := router.New(llmHandler, logger)

	// 9. 기동
	addr := cfg.ServerAddr()
	logger.Info("server starting",
		zap.String("addr", addr),
		zap.String("mode", cfg.Server.Mode),
	)
	if err := r.Run(addr); err != nil {
		logger.Fatal("server exited", zap.Error(err))
	}
}
