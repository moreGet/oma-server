---
name: go-api-data
description: "Go API 서버 데이터 레이어 구현 스킬. 도메인 모델 struct 정의, 레포지토리 인터페이스 구현, DB 마이그레이션 SQL 작성을 수행한다. 'Go 모델 만들어줘', '레포지토리 구현', 'DB 스키마 설계', 'GORM 모델', 'sqlx 쿼리', '마이그레이션 파일', 'Go 엔티티 정의' 등 데이터 레이어 관련 요청 시 반드시 이 스킬을 사용할 것."
---

## 역할

Go API 서버의 데이터 레이어를 구현한다. 아키텍트가 정의한 인터페이스를 실제 코드로 채운다.

## 실행 순서

1. **설계 문서 읽기** — `_workspace/01_architect_design.md`에서 기술 스택·인터페이스 확인
2. **도메인 모델 작성** — `internal/model/`에 struct 생성
3. **레포지토리 구현** — `internal/repository/`에 인터페이스 + 구현체 생성
4. **마이그레이션 작성** — `migrations/`에 SQL 파일 생성
5. **요약 작성** — `_workspace/02_data_summary.md`에 구현 완료 목록 기록

## 도메인 모델 패턴

```go
// internal/model/user.go
package model

import "time"

type User struct {
    ID        int64      `json:"id"         db:"id"         gorm:"primaryKey;autoIncrement"`
    Email     string     `json:"email"      db:"email"      gorm:"uniqueIndex;not null"`
    Password  string     `json:"-"          db:"password"   gorm:"not null"`
    Name      string     `json:"name"       db:"name"       gorm:"not null"`
    CreatedAt time.Time  `json:"created_at" db:"created_at" gorm:"autoCreateTime"`
    UpdatedAt time.Time  `json:"updated_at" db:"updated_at" gorm:"autoUpdateTime"`
    DeletedAt *time.Time `json:"deleted_at" db:"deleted_at" gorm:"index"`
}
```

## 레포지토리 구현 패턴 (GORM)

```go
// internal/repository/user_repository.go
package repository

import (
    "context"
    "errors"
    "gorm.io/gorm"
    "OhMyAgent.AiAgent.Server/internal/model"
    "OhMyAgent.AiAgent.Server/pkg/apperror"
)

type userRepository struct {
    db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
    return &userRepository{db: db}
}

func (r *userRepository) FindByID(ctx context.Context, id int64) (*model.User, error) {
    var user model.User
    if err := r.db.WithContext(ctx).First(&user, id).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, apperror.ErrNotFound
        }
        return nil, err
    }
    return &user, nil
}
```

## 마이그레이션 파일 규칙

- 파일명: `{순서번호}_{설명}.{up|down}.sql`
- up: 스키마 생성/변경, down: 롤백
- 외래 키는 `ON DELETE CASCADE` 또는 `ON DELETE SET NULL` 명시

```sql
-- migrations/001_create_users.up.sql
CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    email      VARCHAR(255) NOT NULL UNIQUE,
    password   VARCHAR(255) NOT NULL,
    name       VARCHAR(100) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);
```

## 공용 에러 타입

```go
// pkg/apperror/error.go
package apperror

import "errors"

var (
    ErrNotFound     = errors.New("not found")
    ErrUnauthorized = errors.New("unauthorized")
    ErrConflict     = errors.New("conflict")
    ErrForbidden    = errors.New("forbidden")
)
```

DB 연결 초기화 패턴은 `references/db-init.md` 참조.
