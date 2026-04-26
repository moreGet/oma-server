---
name: go-api-implementer
description: "Go API 서버 HTTP 레이어 및 비즈니스 로직 구현 스킬. 핸들러, 라우터, 서비스 레이어, DTO, 미들웨어를 작성한다. 'Go 핸들러 만들어줘', 'API 엔드포인트 구현', 'Gin 라우터 설정', 'JWT 미들웨어', '서비스 레이어', 'REST API 구현', 'CRUD 엔드포인트', 'Go 미들웨어' 등 API 구현 관련 요청 시 반드시 이 스킬을 사용할 것."
---

## 역할

Go API 서버의 HTTP 레이어와 비즈니스 로직을 구현한다. 핸들러는 얇게, 서비스 레이어에 로직을 집중시킨다.

## 실행 순서

1. **설계 읽기** — `_workspace/01_architect_design.md`, `_workspace/02_data_summary.md` 확인
2. **DTO 작성** — `internal/dto/`에 요청/응답 struct 생성
3. **서비스 레이어** — `internal/service/`에 인터페이스 + 구현체 생성
4. **핸들러 구현** — `internal/handler/`에 HTTP 핸들러 생성
5. **미들웨어 작성** — `internal/middleware/`에 필요한 미들웨어 생성
6. **라우터 설정** — `internal/router/router.go`에 라우트 등록
7. **main.go 작성** — `cmd/server/main.go`에 의존성 조립
8. **요약 작성** — `_workspace/03_implementer_summary.md`에 엔드포인트 목록 기록

## DTO 패턴

```go
// internal/dto/user.go
package dto

type CreateUserRequest struct {
    Email    string `json:"email"    binding:"required,email"`
    Password string `json:"password" binding:"required,min=8"`
    Name     string `json:"name"     binding:"required,min=2,max=50"`
}

type UserResponse struct {
    ID        int64  `json:"id"`
    Email     string `json:"email"`
    Name      string `json:"name"`
    CreatedAt string `json:"created_at"`
}

type ErrorResponse struct {
    Error   string `json:"error"`
    Details any    `json:"details,omitempty"`
}
```

## 핸들러 패턴 (Gin)

```go
// internal/handler/user_handler.go
package handler

import (
    "net/http"
    "strconv"
    "github.com/gin-gonic/gin"
    "OhMyAgent.AiAgent.Server/internal/dto"
    "OhMyAgent.AiAgent.Server/internal/service"
    "OhMyAgent.AiAgent.Server/pkg/apperror"
    "errors"
)

type UserHandler struct {
    svc service.UserService
}

func NewUserHandler(svc service.UserService) *UserHandler {
    return &UserHandler{svc: svc}
}

func (h *UserHandler) GetUser(c *gin.Context) {
    id, err := strconv.ParseInt(c.Param("id"), 10, 64)
    if err != nil {
        c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid id"})
        return
    }

    user, err := h.svc.GetUser(c.Request.Context(), id)
    if err != nil {
        if errors.Is(err, apperror.ErrNotFound) {
            c.JSON(http.StatusNotFound, dto.ErrorResponse{Error: "user not found"})
            return
        }
        c.JSON(http.StatusInternalServerError, dto.ErrorResponse{Error: "internal server error"})
        return
    }

    c.JSON(http.StatusOK, user)
}

func (h *UserHandler) CreateUser(c *gin.Context) {
    var req dto.CreateUserRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
        return
    }

    resp, err := h.svc.CreateUser(c.Request.Context(), &req)
    if err != nil {
        if errors.Is(err, apperror.ErrConflict) {
            c.JSON(http.StatusConflict, dto.ErrorResponse{Error: "email already exists"})
            return
        }
        c.JSON(http.StatusInternalServerError, dto.ErrorResponse{Error: "internal server error"})
        return
    }

    c.JSON(http.StatusCreated, resp)
}
```

## 라우터 패턴

```go
// internal/router/router.go
package router

import (
    "github.com/gin-gonic/gin"
    "OhMyAgent.AiAgent.Server/internal/handler"
    "OhMyAgent.AiAgent.Server/internal/middleware"
)

func New(userHandler *handler.UserHandler) *gin.Engine {
    r := gin.New()
    r.Use(middleware.Recovery())
    r.Use(middleware.Logger())
    r.Use(middleware.CORS())

    v1 := r.Group("/api/v1")
    {
        users := v1.Group("/users")
        {
            users.GET("/:id", userHandler.GetUser)
            users.GET("", userHandler.ListUsers)
            users.POST("", userHandler.CreateUser)
            users.PUT("/:id", userHandler.UpdateUser)
            users.DELETE("/:id", userHandler.DeleteUser)
        }
    }

    return r
}
```

미들웨어 구현 패턴은 `references/middleware-patterns.md` 참조.
