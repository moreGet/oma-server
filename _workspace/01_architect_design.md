# 01. 아키텍처 설계 — TEMPLATE-SPEC 재정렬(re-align) 타깃

> 본 문서는 기존 `OhMyAgent.AiAgent.Server`를 `TEMPLATE-SPEC.md`(§0~§12)에 맞춰 **전면 재구성**하기 위한 타깃 설계다.
> 코드는 작성하지 않으며, 다음 단계(data-engineer, implementer)가 추가 결정 없이 구현할 수 있도록 import 경로·패키지명·인터페이스 시그니처를 정밀하게 확정한다.
> 우선순위: `CLAUDE.md` > `TEMPLATE-SPEC.md` > 본 설계 > 개별 스킬. **스킬 references의 기본 스택(gin/gorm/postgres/viper/zap)은 스펙과 충돌하므로 전부 무효**, 아래 확정 스택을 따른다.

---

## 0. 확정 결정 요약 (변경 금지)

| 항목 | 결정 |
|------|------|
| 모듈 경로 | `aiagent` |
| 소스 루트 | `com/ohmyagent/` (module=aiagent, org=ohmyagent) |
| import 루트 | `aiagent/com/ohmyagent/internal/...` |
| Go 버전 | `go 1.25` |
| 라우팅 | 표준 `net/http` (Go 1.22+ 메서드·path 패턴, `mux.HandleFunc("GET /api/v1/...")`) |
| 로깅 | 표준 `log/slog` |
| 마이그레이션 | `github.com/pressly/goose/v3` + `//go:embed` |
| DB 드라이버 | 운영 `mysql`, 개발/테스트 `modernc.org/sqlite` (CGO-free) |
| 시각 저장 | `BIGINT` unix seconds |
| 엔티티 PK | `VARCHAR(36)` UUID v4 / 마스터데이터(roles) `INT` |
| 설정 | `configs/{APP_ENV}.yaml` (local/dev/docker/prod) + `config.Load()` 검증 |
| 인증 | JWT(Bearer) + 라우트 레벨 RBAC(3단계) + 유스케이스 게이트 |
| 어그리거트 | `auth`, `llmprovider` (+ 마스터데이터 `roles`는 auth 도메인 내부 모델) |
| 제거 스택 | gin, zap, gorm, golang-migrate, **sqlc 생성코드** |

---

## 1. 타깃 디렉터리 트리

```
OhMyAgent.AiAgent.Server/
├── go.mod                                          # module aiagent, go 1.25
├── go.sum
├── configs/
│   ├── local.yaml
│   ├── dev.yaml
│   ├── docker.yaml
│   └── prod.yaml                                   # 비밀은 빈 값/주석, 실값은 env 주입
├── com/
│   └── ohmyagent/
│       ├── cmd/
│       │   └── api/
│       │       └── main.go                         # 조립 루트(DI)+라우팅+graceful shutdown
│       └── internal/
│           ├── config/
│           │   └── config.go                       # YAML 로드+validate()+SeedsInitialAdmin()
│           ├── logger/
│           │   └── logger.go                       # slog.New 초기화(level/format)
│           ├── domain/
│           │   ├── auth/
│           │   │   ├── model.go                    # Member/Role/RoleLevel/Claims/커맨드/에러
│           │   │   ├── port.go                     # Service/Repository/RoleRepository/TokenService/PasswordHasher
│           │   │   └── service.go                  # CanControl 등 순수 판정(model.go에 둘 수도)
│           │   └── llmprovider/
│           │       ├── model.go                    # LLMProvider/ProviderConfig/ProviderType/커맨드/에러
│           │       ├── port.go                     # Service/Repository/Cache/Factory/Adapter
│           │       └── service.go                  # (선택) 순수 분기 로직
│           ├── application/
│           │   ├── auth/
│           │   │   └── usecase.go                  # AuthUseCase: 로그인/멤버관리/게이트
│           │   └── llmprovider/
│           │       └── usecase.go                  # ProviderService: CRUD+활성화+캐시조율
│           └── adapter/
│               ├── in/
│               │   └── http/
│               │       ├── handler.go              # Handle(), writeJSON, AppError, atoiDefault
│               │       ├── auth_handler.go         # Login + 멤버관리 핸들러 + DTO + 매핑 + authErrToHTTP
│               │       ├── llmprovider_handler.go  # provider 핸들러 + DTO + 매핑 + providerErrToHTTP
│               │       └── security/
│               │           ├── router.go           # SecureRouter, Public/Secured, MinRole
│               │           ├── claims.go           # ClaimsFrom(ctx)/withClaims, ctxKey
│               │           ├── jwt.go              # JWTTokenService(domainauth.TokenService 구현)
│               │           └── middleware.go       # logging + CORS(rs/cors) 체인
│               └── out/
│                   ├── db/
│                   │   ├── db.go                   # Open(driver,dsn)+풀 설정+gooseDialect 분기
│                   │   ├── migrate.go              # //go:embed migrations + RunMigrations
│                   │   ├── helpers.go              # rowScanner/nullString
│                   │   ├── role_repository.go      # domainauth.RoleRepository 구현
│                   │   ├── member_repository.go    # domainauth.Repository 구현
│                   │   ├── llmprovider_repository.go  # domainllmprovider.Repository 구현(손작성)
│                   │   └── migrations/
│                   │       ├── 00001_create_roles.sql
│                   │       ├── 00002_create_members.sql
│                   │       └── 00003_create_llm_providers.sql
│                   ├── auth/
│                   │   └── bcrypt_hasher.go        # domainauth.PasswordHasher 구현(bcrypt)
│                   └── llm/
│                       ├── factory.go              # domainllmprovider.Factory 구현
│                       ├── cache.go                # domainllmprovider.Cache 구현(atomic.Value)
│                       ├── ollama_adapter.go       # Adapter 구현(LOCAL)
│                       ├── claude_adapter.go       # Adapter 구현(EXTERNAL/claude)
│                       └── openai_adapter.go       # Adapter 구현(EXTERNAL/기타)
└── docs/
    └── API-SPEC.md                                 # (구현 단계에서 엔드포인트 채움)
```

> 비고: `pkg/apperror`는 제거하고 AppError를 `adapter/in/http`로 흡수(스펙 §5.2의 `AppError`가 HTTP 레이어 소속). 기존 `internal/middleware`, `internal/router`, `internal/dto`, `internal/handler`, `internal/port`, `internal/infrastructure`는 전부 신규 구조로 흡수/제거(§8 매핑 참조).

---

## 2. go.mod 최종본

```go
module aiagent

go 1.25

require (
	github.com/go-sql-driver/mysql v1.8.1
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/pressly/goose/v3 v3.21.1
	github.com/rs/cors v1.11.1
	golang.org/x/crypto v0.24.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.30.1
)

// 테스트용(테스트 변경은 사용자 승인 후)
require github.com/stretchr/testify v1.9.0
```

제거: `github.com/gin-gonic/gin`, `go.uber.org/zap`(+ multierr), `github.com/golang-migrate/migrate/v4`, gin/validator 트랜잭티브 의존성 전부.

**JWT 라이브러리 선정:** `github.com/golang-jwt/jwt/v5` (HS256, `jwt_secret` 대칭키). 사실상 표준이며 스펙 §11의 "검증된 JWT 라이브러리" 허용 범위. (대안: 표준 crypto 자작 HMAC-JWT — §9 리스크 참조.)
**비밀번호 해시:** `golang.org/x/crypto/bcrypt` (멤버 인증용. 스펙은 명시 안 했으나 로그인 구현에 필수).

---

## 3. 패키지 import alias 표

| 패키지 경로 (`aiagent/com/ohmyagent/...`) | alias | 패키지 선언명 |
|---|---|---|
| `internal/domain/auth` | `domainauth` | `package auth` |
| `internal/application/auth` | `authapp` | `package authapp` |
| `internal/domain/llmprovider` | `domainllmprovider` | `package llmprovider` |
| `internal/application/llmprovider` | `llmproviderapp` | `package llmproviderapp` |
| `internal/adapter/in/http` | `httpin` | `package httpin` |
| `internal/adapter/in/http/security` | `security` | `package security` |
| `internal/adapter/out/db` | `dbout` | `package db` |
| `internal/adapter/out/llm` | `llmout` | `package llm` |
| `internal/adapter/out/auth` | `authout` | `package authout` |
| `internal/config` | `config` | `package config` |
| `internal/logger` | `logger` | `package logger` |

> `out/db`(package `db`)와 `out/llm`(package `llm`)는 import 시 위 alias를 부여해 가독성·충돌 회피. `out/auth`는 도메인 `auth`와 구분 위해 `package authout`.

---

## 4. 포트 인터페이스 계약 (정확한 시그니처)

### 4.1 `domain/auth/port.go`

```go
package auth

import "context"

// Service — in 포트(유스케이스가 노출하는 능력). main.go가 핸들러에 주입.
type Service interface {
	// 인증
	Login(ctx context.Context, cmd LoginCommand) (string, Member, error) // 토큰, 멤버, err

	// 멤버 관리(§8.1)
	ListMembers(ctx context.Context, actorID string, filter MemberFilter) ([]Member, int, error)
	GetMember(ctx context.Context, actorID, targetID string) (Member, error)
	CreateMember(ctx context.Context, cmd CreateMemberCommand) (Member, error)
	ChangeRole(ctx context.Context, cmd ChangeRoleCommand) (Member, error)
	SetActive(ctx context.Context, cmd SetActiveCommand) (Member, error)
	DeleteMember(ctx context.Context, actorID, targetID string) error

	// 인가 게이트(타 도메인이 accessGate로 재사용; §4.4)
	RequireActiveMember(ctx context.Context, actorID string) (Member, error)
	RequireAdmin(ctx context.Context, actorID string) error
	EnsureProjectAccess(ctx context.Context, actorID string, projectID int) error
}

// Repository — out 포트(멤버 영속화).
type Repository interface {
	Save(ctx context.Context, m Member) error                              // INSERT
	Update(ctx context.Context, m Member) error                            // UPDATE(role/active/audit)
	FindByID(ctx context.Context, id string) (Member, error)               // 없으면 ErrNotFound
	FindByUsername(ctx context.Context, username string) (Member, error)   // 없으면 ErrNotFound
	List(ctx context.Context, filter MemberFilter) ([]Member, int, error)  // total 포함
	Delete(ctx context.Context, id string) error                           // 0행→ErrNotFound
}

// RoleRepository — out 포트(마스터데이터 roles).
type RoleRepository interface {
	FindByID(ctx context.Context, id int) (Role, error)   // 없으면 ErrNotFound
	List(ctx context.Context) ([]Role, error)
}

// TokenService — out 포트(JWT 발급/검증). security.JWTTokenService가 구현.
type TokenService interface {
	Generate(member Member) (string, error)
	Parse(token string) (Claims, error)   // 실패 시 ErrInvalidToken
}

// PasswordHasher — out 포트(bcrypt). authout.BcryptHasher가 구현.
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) error   // 불일치 시 ErrInvalidCredentials
}
```

### 4.2 `domain/llmprovider/port.go`

```go
package llmprovider

import "context"

// Service — in 포트.
type Service interface {
	List(ctx context.Context, actorID string) ([]LLMProvider, error)
	Get(ctx context.Context, actorID, id string) (LLMProvider, error)
	Create(ctx context.Context, cmd CreateCommand) (LLMProvider, error)
	UpdateConfig(ctx context.Context, cmd UpdateConfigCommand) (LLMProvider, error)
	Activate(ctx context.Context, cmd ActivateCommand) error
	Delete(ctx context.Context, cmd DeleteCommand) error
	GetActiveAdapter(ctx context.Context) (Adapter, error) // 캐시→DB 폴백→팩토리
}

// Repository — out 포트(영속화). 손으로 쓴 repository(sqlc 제거).
type Repository interface {
	GetActive(ctx context.Context) (LLMProvider, error)            // 없으면 ErrNoActiveProvider
	FindByID(ctx context.Context, id string) (LLMProvider, error)  // 없으면 ErrNotFound
	List(ctx context.Context) ([]LLMProvider, error)
	Save(ctx context.Context, p LLMProvider) error                 // INSERT
	UpdateConfig(ctx context.Context, id string, cfg ProviderConfig, updatedAt int64, updatedBy string) error // 0행→ErrNotFound
	Activate(ctx context.Context, id string, now int64, actorID string) error // tx: 전체 비활성→지정 활성, 0행→ErrNotFound
	Delete(ctx context.Context, id string) error                   // 0행→ErrNotFound
}

// Cache — out 포트(활성 Provider 캐시, atomic.Value 기반).
type Cache interface {
	Get() (LLMProvider, bool)
	Set(p LLMProvider)
	Invalidate()
}

// Factory — out 포트(Provider→Adapter 인스턴스화).
type Factory interface {
	CreateAdapter(p LLMProvider) (Adapter, error)
}

// Adapter — out 포트(LLM 벤더 한 인스턴스).
type Adapter interface {
	Complete(ctx context.Context, prompt string) (string, error)
	ProviderType() ProviderType
}
```

> **이전 대비 변경점:** 기존 `port.LLMRepository`/`LLMAdapter`/`LLMFactory`/`ProviderCache`(internal/port)와 기능 동등하되,
> (1) 어그리거트 내부(`domain/llmprovider/port.go`)로 이동, (2) 포인터 반환 → **값 반환**으로 통일(스펙 §3 스타일), (3) PK가 `int64`→`string`(UUID)로 변경되며 시그니처의 `id int64`→`id string`, (4) Cache는 `bool` 동반 반환으로 nil-pointer 제거, (5) 감사필드(updatedBy/actorID) 인자 추가.

---

## 5. 도메인 모델 스케치

### 5.1 `domain/auth/model.go`

```go
package auth

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러(핸들러가 HTTP로 매핑) ---
var ErrNotFound = errors.New("member not found")             // → 404
var ErrPermission = errors.New("permission denied")          // → 403
var ErrInvalidCredentials = errors.New("invalid credentials")// → 401
var ErrInvalidToken = errors.New("invalid token")            // → 401
var ErrConflict = errors.New("member already exists")        // → 409

type ErrValidation struct{ Msg string }                       // → 400
func (e *ErrValidation) Error() string { return e.Msg }

// --- 역할 계층(§4.1) ---
type RoleLevel int
const (
	RoleLevelUser       RoleLevel = 0
	RoleLevelAdmin      RoleLevel = 1
	RoleLevelSuperAdmin RoleLevel = 2
)
// 상위만 하위를 제어.
func (actor RoleLevel) CanControl(target RoleLevel) bool { return actor > target }

// role_id(1/2/3, DB·API 노출) ↔ role_level(0/1/2, 코드 비교) 구분.
type Role struct {
	ID    int       // 1=user,2=admin,3=super_admin
	Name  string    // "user"/"admin"/"super_admin"
	Level RoleLevel // 0/1/2
}

// --- 엔티티 ---
type Member struct {
	ID           string // UUID v4
	Username     string
	PasswordHash string // bcrypt; 응답 DTO 노출 금지
	Active       bool
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CreatedBy    string // "" = 시스템
	UpdatedBy    string
}

// --- JWT 클레임(§4.6) ---
type Claims struct {
	MemberID string
	Username string
	Level    RoleLevel
}

// --- 커맨드 ---
type LoginCommand struct{ Username, Password string }

type CreateMemberCommand struct {
	Username string
	Password string
	RoleID   int
	ActorID  string
}
type ChangeRoleCommand struct{ ActorID, TargetID string; RoleID int }
type SetActiveCommand  struct{ ActorID, TargetID string; Active bool }
type MemberFilter      struct{ RoleID int; Limit, Offset int } // RoleID 0=전체

func (c *CreateMemberCommand) Normalize() { c.Username = strings.TrimSpace(c.Username) }
func (c *CreateMemberCommand) Validate() error {
	c.Normalize()
	if c.Username == "" { return &ErrValidation{Msg: "username is required"} }
	if len(c.Password) < 8 { return &ErrValidation{Msg: "password must be >= 8 chars"} }
	if c.RoleID < 1 || c.RoleID > 3 { return &ErrValidation{Msg: "invalid role_id"} }
	return nil
}
```

### 5.2 `domain/llmprovider/model.go` — 기존 타입 보존 매핑

기존 `domain.LLMProvider`/`ProviderConfig`/`ProviderType`을 **타입·JSON 태그 그대로 보존**하되, 스펙 §6에 맞춰 PK·시각만 조정한다.

```go
package llmprovider

import (
	"errors"
	"strings"
	"time"
)

var ErrNotFound = errors.New("llm provider not found")        // → 404
var ErrNoActiveProvider = errors.New("no active llm provider")// → 404
var ErrConflict = errors.New("provider already exists")       // → 409
type ErrValidation struct{ Msg string }                        // → 400
func (e *ErrValidation) Error() string { return e.Msg }

type ProviderType string
const (
	ProviderTypeLocal    ProviderType = "LOCAL"
	ProviderTypeExternal ProviderType = "EXTERNAL"
)

// 기존 ProviderConfig 보존(JSON 태그 동일).
type ProviderConfig struct {
	Endpoint    string         `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	APIKeyEnv   string         `json:"api_key_env,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

// 변경점: ID int64 → string(UUID), 감사필드 추가.
type LLMProvider struct {
	ID           string // (구) int64 → UUID v4
	Name         string
	IsActive     bool
	ProviderType ProviderType
	Config       ProviderConfig
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CreatedBy    string
	UpdatedBy    string
}

type CreateCommand struct {
	Name         string
	ProviderType ProviderType
	IsActive     bool
	Config       ProviderConfig
	ActorID      string
}
type UpdateConfigCommand struct{ ID string; Config ProviderConfig; ActorID string }
type ActivateCommand     struct{ ID, ActorID string }
type DeleteCommand       struct{ ID, ActorID string }

func (c *CreateCommand) Normalize() { c.Name = strings.TrimSpace(c.Name) }
func (c *CreateCommand) Validate() error {
	c.Normalize()
	if c.Name == "" { return &ErrValidation{Msg: "name is required"} }
	if c.ProviderType != ProviderTypeLocal && c.ProviderType != ProviderTypeExternal {
		return &ErrValidation{Msg: "provider_type must be LOCAL or EXTERNAL"}
	}
	return nil
}
```

**보존/변경 매핑표 (llmprovider 도메인 타입)**

| 기존 | 신규 | 처리 |
|---|---|---|
| `domain.ProviderType` + 상수 | `llmprovider.ProviderType` | 보존(이동만) |
| `domain.ProviderConfig` | `llmprovider.ProviderConfig` | 보존(JSON 태그 동일) |
| `domain.LLMProvider` | `llmprovider.LLMProvider` | ID `int64→string`, 감사필드 추가 |
| `domain.ErrProviderNotFound` | `llmprovider.ErrNotFound` | 이름 변경 |
| `domain.ErrNoActiveProvider` | `llmprovider.ErrNoActiveProvider` | 보존 |
| `domain.ErrInvalidProvider`/`ErrInvalidProviderType` | `llmprovider.ErrValidation` | typed 에러로 통합 |
| `domain.ErrProviderConflict` | `llmprovider.ErrConflict` | 보존 |
| `LLMProvider.Validate()` | `CreateCommand.Validate()` | 커맨드로 이동(스펙 §3.1) |

---

## 6. DB 마이그레이션 계획 (goose + embed)

파일명 규칙: `NNNNN_description.sql`(5자리). 각 파일은 `-- +goose Up` / `-- +goose Down`. `RunMigrations(ctx, driver, conn)`이 기동 시 자동 Up. `gooseDialect(driver)`로 mysql/sqlite 분기. 시각=`BIGINT` unix, 엔티티 PK=`VARCHAR(36)`, 마스터=`INT`.

### 00001_create_roles.sql (마스터데이터, INT PK)
```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS roles (
    id    INT          NOT NULL PRIMARY KEY,   -- 1/2/3
    name  VARCHAR(50)  NOT NULL UNIQUE,        -- user/admin/super_admin
    level INT          NOT NULL                -- 0/1/2 (RoleLevel)
);
INSERT INTO roles (id, name, level) VALUES (1,'user',0),(2,'admin',1),(3,'super_admin',2);
-- +goose Down
DROP TABLE IF EXISTS roles;
```

### 00002_create_members.sql (§6.1 스키마 규칙)
```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS members (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,  -- UUID v4
    username      VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    active        BOOLEAN      NOT NULL DEFAULT TRUE,
    role_id       INT          NOT NULL,
    created_at    BIGINT       NOT NULL,              -- unix seconds
    updated_at    BIGINT       NOT NULL,
    created_by    VARCHAR(36),                        -- NULL=시스템
    updated_by    VARCHAR(36),
    FOREIGN KEY (role_id) REFERENCES roles(id)
);
CREATE INDEX idx_members_role_id ON members(role_id);
-- +goose Down
DROP TABLE IF EXISTS members;
```

### 00003_create_llm_providers.sql (기존 001/002 대체)
```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS llm_providers (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,  -- (구)BIGINT AUTO_INCREMENT → UUID
    name          VARCHAR(100) NOT NULL,
    is_active     BOOLEAN      NOT NULL DEFAULT FALSE, -- (구)TINYINT(1)
    provider_type VARCHAR(20)  NOT NULL,               -- (구)ENUM → VARCHAR(sqlite 호환)
    config_json   TEXT         NOT NULL,               -- (구)JSON → TEXT(sqlite 호환, app에서 JSON 직렬화)
    created_at    BIGINT       NOT NULL,
    updated_at    BIGINT       NOT NULL,
    created_by    VARCHAR(36),
    updated_by    VARCHAR(36)
);
CREATE INDEX idx_llm_providers_is_active ON llm_providers(is_active);
-- +goose Down
DROP TABLE IF EXISTS llm_providers;
```

> **MySQL/SQLite 양립 결정:**
> - `ENUM` → `VARCHAR(20)`(sqlite는 ENUM 미지원, 값 검증은 도메인 `Validate()`가 담당).
> - `JSON` → `TEXT`(sqlite 호환; app에서 `json.Marshal/Unmarshal`. MySQL도 TEXT로 충분, JSON 함수 미사용).
> - `TIMESTAMP DEFAULT CURRENT_TIMESTAMP` → `BIGINT`(스펙 §6.1; 시각은 app에서 `t.Unix()` 채움).
> - `TINYINT(1)` → `BOOLEAN`(sqlite/mysql 양립).
> - 샘플 시드(기존 002)는 운영 마이그레이션에서 제거. 로컬/개발 샘플 Provider 시딩은 app 코드로(아래 §9-3) 이전.
> - **AUTO_INCREMENT 제거**: PK 생성은 app(`uuid.NewString()`)이 담당.

기존 `migrations/migrations.go`(golang-migrate용 `*.sql` embed)는 **삭제**, 신규 `adapter/out/db/migrate.go`가 goose용 `migrations/*.sql`을 embed.

---

## 7. 라우트 표

`SecureRouter`로 등록. `MinRole`은 라우트 최소 게이트, 세밀 인가는 유스케이스. 경로 패턴은 Go 1.22+ `"METHOD /path/{id}"`.

| 메서드·경로 | MinRole | 핸들러 | 유스케이스 추가 게이트 |
|---|---|---|---|
| `POST /api/v1/auth/login` | Public | `authH.Login` | — |
| `GET /api/v1/members` | admin | `authH.ListMembers` | RequireAdmin |
| `POST /api/v1/members` | admin | `authH.CreateMember` | RequireAdmin + CanControl(생성 역할) |
| `GET /api/v1/members/{id}` | user | `authH.GetMember` | 본인 또는 admin↑ |
| `PUT /api/v1/members/{id}/role` | admin | `authH.ChangeRole` | RequireAdmin + `actor.CanControl(target)` |
| `PUT /api/v1/members/{id}/active` | admin | `authH.SetActive` | RequireAdmin + CanControl |
| `DELETE /api/v1/members/{id}` | **super_admin** | `authH.DeleteMember` | CanControl |
| `GET /api/v1/llm-providers` | user | `provH.List` | — (§8.3 조회는 user) |
| `GET /api/v1/llm-providers/{id}` | user | `provH.Get` | — |
| `POST /api/v1/llm-providers` | admin | `provH.Create` | RequireAdmin |
| `PATCH /api/v1/llm-providers/{id}/config` | admin | `provH.UpdateConfig` | RequireAdmin |
| `PUT /api/v1/llm-providers/{id}/activate` | admin | `provH.Activate` | RequireAdmin |
| `DELETE /api/v1/llm-providers/{id}` | admin | `provH.Delete` | RequireAdmin (삭제는 admin, 멤버삭제만 super_admin) |

> 기존은 모두 `/api/v1/admin/llm-providers`(인증 없음). 신규는 스펙 §8.3을 따라 조회=user, 변경=admin으로 분리하고 `admin` prefix 제거.

---

## 8. 기존 → 신규 파일 이전 매핑

| 기존 경로 | 신규 경로 | 구분 |
|---|---|---|
| `cmd/server/main.go` | `com/ohmyagent/cmd/api/main.go` | 재작성(net/http+slog+goose+DI 전면 교체) |
| `internal/config/config.go` | `com/ohmyagent/internal/config/config.go` | 재작성(env별 YAML, validate, Auth/Security 섹션, SeedsInitialAdmin) |
| (신규) | `com/ohmyagent/internal/logger/logger.go` | 신규(slog) |
| `internal/domain/llm_provider.go` | `com/ohmyagent/internal/domain/llmprovider/model.go` | 재배치+조정(타입 보존, ID→string) |
| `internal/domain/errors.go` | `→ llmprovider/model.go`에 흡수 | 통합 |
| `internal/port/llm_repository.go`, `llm_adapter.go` | `com/ohmyagent/internal/domain/llmprovider/port.go` | 재배치(어그리거트 내부로) |
| `internal/application/llm_provider_service.go` | `com/ohmyagent/internal/application/llmprovider/usecase.go` | 재작성(값 반환, 게이트, 감사필드) |
| `internal/adapter/db/llm_provider_repository.go` | `com/ohmyagent/internal/adapter/out/db/llmprovider_repository.go` | 재작성(**sqlc 제거**, 손작성 SQL) |
| `internal/adapter/db/sqlc/*` | — | **삭제**(sqlc 폐기) |
| `internal/adapter/cache/provider_cache.go` | `com/ohmyagent/internal/adapter/out/llm/cache.go` | 재배치(Cache 시그니처 bool 동반) |
| `internal/adapter/llm/factory.go` | `com/ohmyagent/internal/adapter/out/llm/factory.go` | 재배치(보존) |
| `internal/adapter/llm/{ollama,claude,openai}_adapter.go` | `com/ohmyagent/internal/adapter/out/llm/*_adapter.go` | 재배치(보존) |
| `internal/dto/llm_provider.go` | `→ adapter/in/http/llmprovider_handler.go` 하단 | 흡수(DTO를 핸들러 파일로, binding 태그→수동 검증) |
| `internal/handler/llm_provider_handler.go` | `com/ohmyagent/internal/adapter/in/http/llmprovider_handler.go` | 재작성(gin→`func(w,r)error`, respondError→providerErrToHTTP) |
| `internal/router/router.go` | `→ security/router.go` + `main.go` | 재작성(SecureRouter) |
| `internal/middleware/{cors,logger,recovery}.go` | `→ security/middleware.go` + `httpin.Handle()` | 재작성(rs/cors, slog, recover) |
| `pkg/apperror/error.go` | `→ adapter/in/http/handler.go`의 AppError | 흡수(HTTP AppError 모델) |
| `migrations/migrations.go` | `→ adapter/out/db/migrate.go` | 재작성(golang-migrate→goose) |
| `migrations/001_*.up/down.sql`, `002_*` | `adapter/out/db/migrations/00003_create_llm_providers.sql` | 재작성(단일 goose 파일, 스키마 규칙 적용) |
| `db/schema.sql`, `db/query.sql`, `sqlc.yaml` | — | **삭제**(sqlc 입력 폐기) |
| `internal/application/usecase/agentic_loop.go` | — | **삭제(고아)** |
| `internal/infrastructure/adapter/inbound/rest/*` | — | **삭제(고아)** |
| `internal/infrastructure/adapter/outbound/llm/*`, `outbound/mcp/*` | — | **삭제(고아)** |
| `internal/infrastructure/session/*` | — | **삭제(고아)** |
| `internal/infrastructure/db.go` | `→ adapter/out/db/db.go` | 재작성(mysql+sqlite Open, 풀 설정) |
| `internal/infrastructure/migrator.go` | `→ adapter/out/db/migrate.go` | 재작성(goose) |
| `internal/domain/port/{inbound,outbound}.go` | — | **삭제(고아)** |
| `internal/domain/{session,message,tool}.go` | — | **삭제(고아, 에이전트 루프 잔재)** |
| `config/config.yaml` | `configs/{local,dev,docker,prod}.yaml` | 재작성(env별 분리) |
| (신규) | `com/ohmyagent/internal/adapter/out/db/{role,member}_repository.go` | 신규(auth) |
| (신규) | `com/ohmyagent/internal/adapter/out/auth/bcrypt_hasher.go` | 신규(PasswordHasher) |
| (신규) | `com/ohmyagent/internal/domain/auth/*`, `application/auth/usecase.go` | 신규(auth 전체) |
| (신규) | `com/ohmyagent/internal/adapter/in/http/{handler,auth_handler}.go`, `security/*` | 신규 |

### 제거 대상 목록 (명시)
1. `internal/application/usecase/agentic_loop.go`
2. `internal/infrastructure/adapter/inbound/rest/{handler.go,router.go}`
3. `internal/infrastructure/adapter/outbound/llm/ollama_adapter.go`
4. `internal/infrastructure/adapter/outbound/mcp/client_manager.go`
5. `internal/infrastructure/session/memory_store.go`
6. `internal/domain/port/{inbound.go,outbound.go}`
7. `internal/domain/{session.go,message.go,tool.go}` (에이전트 루프 전용 고아)
8. `internal/adapter/db/sqlc/{db.go,models.go,query.sql.go}` + `sqlc.yaml` + `db/{schema.sql,query.sql}` (sqlc 폐기)
9. `pkg/apperror/`, `internal/router/`, `internal/middleware/`, `internal/dto/`, `internal/handler/`, `internal/port/`, `internal/infrastructure/`, `internal/domain/{llm_provider.go,errors.go}` (구조 이전 후 빈 디렉터리 제거)
10. 구 `cmd/server/`, 구 `migrations/`, 구 `config/`

> 비고: 기존 `*_test.go`(provider_cache_test, factory_test, llm_provider_service_test, llm_provider_handler_test)는 모두 구 패키지 경로·gin·int64 ID에 묶여 있어 신규 구조에서 컴파일 불가하다. **테스트 보호 룰(CLAUDE.md §7)에 따라 본 설계에서는 손대지 않고**, 신규 구조 확정 후 사용자 승인 하에 일괄 재작성한다(§9-4).

---

## 9. 리스크 · 미결정 사항

1. **JWT 라이브러리 (권장: `golang-jwt/jwt/v5`)** — 스펙 §11이 "표준 crypto 자작 또는 검증된 라이브러리" 둘 다 허용. 자작 HMAC-JWT는 의존성 0이나 만료/서명 검증 버그 리스크. **결정안: golang-jwt/jwt/v5 채택**(검증·만료·alg 고정이 안전). 자작을 원하면 `security/jwt.go`만 교체하면 되도록 `TokenService` 포트로 격리했다 — implementer 단계 최종 확인 필요.

2. **sqlc 완전 폐기** — 기존 `internal/adapter/db/sqlc/`와 `db/`, `sqlc.yaml`을 모두 삭제하고 손작성 repository로 전환(스펙 §6.3은 단순 CRUD에 sqlc를 "선택"으로만 허용; 양립 드라이버·UUID·감사필드 추가로 손작성이 더 명확). sqlc 유지를 원하면 별도 결정 필요하나, **본 설계는 폐기 확정**.

3. **샘플 Provider 시드 처리 (미결정)** — 기존 `002_seed_sample_providers`는 (a) goose 마이그레이션에 두면 모든 환경에 적용되어 운영 오염, (b) UUID PK 전략과 충돌(고정 id=1,2,3 불가). **권장: app 코드 시딩**(`config.SeedsInitialAdmin()`과 동일 패턴으로 비운영 환경에서만 super_admin 멤버 + 샘플 Provider 생성). data/implementer 단계에서 시드 위치 확정 필요.

4. **테스트 재작성 범위 (사용자 승인 필요)** — 4개 기존 `*_test.go`가 신규 구조와 비호환. 컴파일 통과를 위해 재작성이 불가피하나 테스트 보호 룰상 본 단계에서는 동결. 구현 완료 후 별도 승인 절차로 진행.

5. **Go 미설치 → 빌드 미검증** — 본 환경에서 `go build`/`go vet` 불가. import 경로·시그니처를 정밀 확정했으나, implementer 단계에서 최초 빌드 시 (a) `out/db`·`out/llm` 패키지명 충돌(import alias 필수), (b) goose Provider API 버전(`goose.NewProvider` / `goose.SetBaseFS` v3.21 시그니처) 확인 필요.

6. **EnsureProjectAccess / projectTokens** — 스펙 §4.4 예시는 GitLab 프로젝트 토큰 전제. 본 프로젝트엔 project 개념이 없으므로 `EnsureProjectAccess`는 포트에 시그니처만 두되 구현은 `RequireAdmin`으로 위임(또는 미구현 stub). 향후 도메인 추가 시 확장. **현재는 admin↑ 통과로 단순화 권장.**

7. **CORS/CSRF** — 스펙 §5.6은 CORS+CSRF 체인을 요구하나, 본 프로젝트는 토큰 기반(Bearer) API라 CSRF 위험이 낮다. **결정안: `rs/cors`만 적용**(AllowedOrigins 비면 비활성), CSRF는 쿠키 세션 도입 시 추가. config `security` 섹션에 `allowed_origins`만 둔다.

---

## 부록 A. config 스키마 (configs/{env}.yaml)

```yaml
server:
  port: 8080
  read_timeout: "10s"
  write_timeout: "10s"
security:
  allowed_origins: []          # 비면 CORS 비활성(로컬)
database:
  driver: "sqlite"             # local/dev/test=sqlite, docker/prod=mysql
  dsn: "file:aiagent.db?_pragma=busy_timeout(5000)"  # mysql 예: user:pw@tcp(host:3306)/db?parseTime=true
  max_open_conns: 1            # sqlite 단일 writer; mysql은 10+
auth:
  jwt_secret: ""               # 하드코딩 금지, env 주입(APP_AUTH_JWT_SECRET 등)
  jwt_expiry: "24h"
  seed_admin_username: "admin" # 비운영 시딩용
  seed_admin_password: ""      # 비운영만, env 주입
```

`Config` 구조체: `Env`(APP_ENV 주입), `Server`, `Security`, `Database{Driver,DSN,MaxOpenConns}`, `Auth{JWTSecret,JWTExpiry(Duration),SeedAdmin...}`. `validate()`는 port>0, driver∈{mysql,sqlite}, 운영환경에서 jwt_secret 비어있으면 에러. `SeedsInitialAdmin()` = `Env ∈ {local,dev,docker}`.
