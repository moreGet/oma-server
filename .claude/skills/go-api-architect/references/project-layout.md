# 프로젝트 레이아웃 표준

## 표준 디렉토리 구조

```
.
├── cmd/
│   └── server/
│       └── main.go          # 진입점, 의존성 조립
├── internal/
│   ├── config/
│   │   └── config.go        # 설정 struct + 로드 함수
│   ├── model/
│   │   └── *.go             # 도메인 모델 struct
│   ├── repository/
│   │   ├── interface.go     # 레포지토리 인터페이스
│   │   └── *.go             # DB 구현체
│   ├── service/
│   │   ├── interface.go     # 서비스 인터페이스
│   │   └── *.go             # 비즈니스 로직
│   ├── handler/
│   │   └── *.go             # HTTP 핸들러
│   ├── middleware/
│   │   └── *.go             # 미들웨어
│   ├── dto/
│   │   └── *.go             # 요청/응답 DTO
│   └── router/
│       └── router.go        # 라우터 설정
├── pkg/
│   └── apperror/
│       └── error.go         # 공용 에러 타입
├── migrations/
│   ├── 001_init.up.sql
│   └── 001_init.down.sql
├── config/
│   └── config.yaml          # 기본 설정 파일
├── go.mod
├── go.sum
└── .env.example
```

## main.go 패턴 (의존성 조립)

```go
package main

import (
    "log"
    "OhMyAgent.AiAgent.Server/internal/config"
    "OhMyAgent.AiAgent.Server/internal/repository"
    "OhMyAgent.AiAgent.Server/internal/service"
    "OhMyAgent.AiAgent.Server/internal/handler"
    "OhMyAgent.AiAgent.Server/internal/router"
)

func main() {
    cfg, err := config.Load()
    if err != nil {
        log.Fatal(err)
    }

    db := initDB(cfg.Database)
    
    // 의존성 조립 (아래에서 위로)
    repo := repository.NewUserRepository(db)
    svc  := service.NewUserService(repo)
    h    := handler.NewUserHandler(svc)
    
    r := router.New(cfg, h)
    r.Run(cfg.Server.Addr)
}
```

## 레포지토리 인터페이스 패턴

```go
// internal/repository/interface.go
package repository

import (
    "context"
    "OhMyAgent.AiAgent.Server/internal/model"
)

type UserRepository interface {
    FindByID(ctx context.Context, id int64) (*model.User, error)
    FindAll(ctx context.Context) ([]*model.User, error)
    Create(ctx context.Context, user *model.User) error
    Update(ctx context.Context, user *model.User) error
    Delete(ctx context.Context, id int64) error
}
```

## 서비스 인터페이스 패턴

```go
// internal/service/interface.go
package service

import (
    "context"
    "OhMyAgent.AiAgent.Server/internal/dto"
)

type UserService interface {
    GetUser(ctx context.Context, id int64) (*dto.UserResponse, error)
    ListUsers(ctx context.Context) ([]*dto.UserResponse, error)
    CreateUser(ctx context.Context, req *dto.CreateUserRequest) (*dto.UserResponse, error)
    UpdateUser(ctx context.Context, id int64, req *dto.UpdateUserRequest) (*dto.UserResponse, error)
    DeleteUser(ctx context.Context, id int64) error
}
```
