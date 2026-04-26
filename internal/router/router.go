package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"OhMyAgent.AiAgent.Server/internal/handler"
	"OhMyAgent.AiAgent.Server/internal/middleware"
)

// New 는 LLM Provider Admin API 라우터를 구성하여 *gin.Engine 을 반환한다.
//
// 미들웨어 순서: Recovery → Logger → CORS
// 라우트:
//
//	GET    /health
//	GET    /api/v1/admin/llm-providers
//	GET    /api/v1/admin/llm-providers/:id
//	POST   /api/v1/admin/llm-providers
//	PUT    /api/v1/admin/llm-providers/:id/activate
//	PATCH  /api/v1/admin/llm-providers/:id/config
//	DELETE /api/v1/admin/llm-providers/:id
func New(llmHandler *handler.LLMProviderHandler, logger *zap.Logger) *gin.Engine {
	r := gin.New()
	r.Use(middleware.Recovery(logger))
	r.Use(middleware.Logger(logger))
	r.Use(middleware.CORS())

	// 헬스체크 (라이브니스)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/api/v1")
	{
		admin := v1.Group("/admin")
		{
			llm := admin.Group("/llm-providers")
			{
				llm.GET("", llmHandler.ListProviders)
				llm.GET("/:id", llmHandler.GetProvider)
				llm.POST("", llmHandler.CreateProvider)
				llm.PUT("/:id/activate", llmHandler.ActivateProvider)
				llm.PATCH("/:id/config", llmHandler.UpdateProviderConfig)
				llm.DELETE("/:id", llmHandler.DeleteProvider)
			}
		}
	}

	return r
}
