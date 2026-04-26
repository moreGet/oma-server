package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Logger 는 요청/응답을 zap 으로 구조화 로깅하는 미들웨어이다.
// method, path, status, latency, client ip 를 기록한다.
func Logger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		fullPath := path
		if raw != "" {
			fullPath = path + "?" + raw
		}

		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", fullPath),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", latency),
			zap.String("client_ip", c.ClientIP()),
		}
		if len(c.Errors) > 0 {
			fields = append(fields, zap.String("errors", c.Errors.String()))
			logger.Error("http", fields...)
			return
		}
		logger.Info("http", fields...)
	}
}
