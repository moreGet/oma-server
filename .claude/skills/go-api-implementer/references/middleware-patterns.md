# 미들웨어 구현 패턴

## Recovery (패닉 복구)

```go
// internal/middleware/recovery.go
package middleware

import (
    "net/http"
    "github.com/gin-gonic/gin"
    "OhMyAgent.AiAgent.Server/internal/dto"
)

func Recovery() gin.HandlerFunc {
    return gin.CustomRecovery(func(c *gin.Context, err any) {
        c.JSON(http.StatusInternalServerError, dto.ErrorResponse{Error: "internal server error"})
    })
}
```

## Logger (구조화 로깅)

```go
// internal/middleware/logger.go
package middleware

import (
    "time"
    "github.com/gin-gonic/gin"
    "go.uber.org/zap"
)

func Logger() gin.HandlerFunc {
    logger, _ := zap.NewProduction()
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()
        logger.Info("request",
            zap.String("method", c.Request.Method),
            zap.String("path", c.Request.URL.Path),
            zap.Int("status", c.Writer.Status()),
            zap.Duration("latency", time.Since(start)),
        )
    }
}
```

## CORS

```go
// internal/middleware/cors.go
package middleware

import (
    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
    "time"
)

func CORS() gin.HandlerFunc {
    return cors.New(cors.Config{
        AllowOrigins:     []string{"*"},
        AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
        AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
        ExposeHeaders:    []string{"Content-Length"},
        AllowCredentials: false,
        MaxAge:           12 * time.Hour,
    })
}
```

## JWT 인증

```go
// internal/middleware/auth.go
package middleware

import (
    "net/http"
    "strings"
    "github.com/gin-gonic/gin"
    "github.com/golang-jwt/jwt/v5"
    "OhMyAgent.AiAgent.Server/internal/dto"
)

func Auth(jwtSecret string) gin.HandlerFunc {
    return func(c *gin.Context) {
        authHeader := c.GetHeader("Authorization")
        if !strings.HasPrefix(authHeader, "Bearer ") {
            c.AbortWithStatusJSON(http.StatusUnauthorized, dto.ErrorResponse{Error: "unauthorized"})
            return
        }

        tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
        token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
            return []byte(jwtSecret), nil
        })
        if err != nil || !token.Valid {
            c.AbortWithStatusJSON(http.StatusUnauthorized, dto.ErrorResponse{Error: "invalid token"})
            return
        }

        claims, _ := token.Claims.(jwt.MapClaims)
        c.Set("user_id", claims["sub"])
        c.Next()
    }
}
```

## Rate Limiter

```go
// golang.org/x/time/rate 사용
import "golang.org/x/time/rate"

var limiter = rate.NewLimiter(rate.Limit(100), 200) // 초당 100, 버스트 200

func RateLimit() gin.HandlerFunc {
    return func(c *gin.Context) {
        if !limiter.Allow() {
            c.AbortWithStatusJSON(http.StatusTooManyRequests, dto.ErrorResponse{Error: "too many requests"})
            return
        }
        c.Next()
    }
}
```
