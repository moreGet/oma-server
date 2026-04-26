package rest

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func NewRouter(h *Handler, logger *zap.Logger) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(logger))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		// 에이전트 API
		api.POST("/sessions", h.CreateSession)
		api.GET("/sessions/:id", h.GetSession)
		api.POST("/chat", h.Chat)

		// C# 클라이언트 관리
		clients := api.Group("/clients")
		{
			clients.POST("/register", h.RegisterClient)
			clients.GET("", h.ListClients)
			clients.GET("/:id/events", h.ClientEvents)  // SSE
			clients.POST("/:id/result", h.ClientResult) // 도구 실행 결과
		}
	}

	return r
}

func requestLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
		)
	}
}
