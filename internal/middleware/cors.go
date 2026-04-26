package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS 는 개발용 단순 CORS 미들웨어이다 (gin-contrib/cors 미설치 환경 대응).
// 모든 Origin 허용, 일반적인 메서드/헤더 허용, preflight 처리.
//
// 운영 환경에서는 AllowedOrigins 화이트리스트로 제한할 것.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin,Content-Type,Accept,Authorization,X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
