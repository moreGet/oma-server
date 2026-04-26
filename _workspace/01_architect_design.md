# 아키텍처 설계

본 문서는 OhMyAgent.AiAgent.Server 의 LLM 제공자 동적 스위칭 시스템의 청사진이다.
헥사고널 아키텍처(Ports & Adapters)를 기반으로, MariaDB 의 `llm_providers` 테이블 한 행의
`is_active` 플래그를 변경하는 것만으로 LLM 백엔드(Ollama / Claude / GPT)를
런타임에 무중단 스위칭할 수 있도록 설계한다.

---

## 기술 스택

| 역할 | 선택 | 이유 |
|------|------|------|
| Language | Go 1.22 | 정적 타입 + 단일 바이너리 + 동시성 우수, 기존 go.mod 와 일치 |
| HTTP Framework | Gin v1.9.1 | 검증된 미들웨어 생태계, 라우팅 성능, 기존 의존성 재사용 |
| Database | MariaDB 10.6+ | JSON 컬럼 지원, ENUM 지원, 운영 안정성 |
| DB Driver | go-sql-driver/mysql v1.8+ | MariaDB 와 와이어 호환, 표준 `database/sql` 인터페이스 |
| Query Codegen | sqlc v1.25+ (engine: mysql) | SQL → Type-safe Go 코드 자동 생성, ORM 오버헤드 제거 |
| 마이그레이션 | golang-migrate/migrate v4 | 양방향 마이그레이션, MariaDB 드라이버 지원 |
| Architecture | Hexagonal (Ports & Adapters) | DB / LLM 벤더 교체 시 도메인 영향 최소화, 테스트 용이 |
| Cache | sync/atomic.Value | lock-free 읽기, 활성 Provider 단일 인스턴스 핫스왑 |
| Logger | go.uber.org/zap | 구조화 로깅, 고성능, 기존 의존성 재사용 |
| Config | spf13/viper | env / yaml / flag 통합 로딩 |
| Validation | go-playground/validator (Gin 내장) | 핸들러 DTO 자동 검증 |
| HTTP Client (LLM) | net/http + 표준 컨텍스트 | 외부 의존성 최소화, 타임아웃·취소 일관 처리 |

---

## 디렉토리 구조

헥사고널 아키텍처를 반영하여 **도메인(중심)** 과 **어댑터(외곽)** 가 명확히 분리되도록 구성한다.
sqlc 가 생성한 코드는 `internal/adapter/db/sqlc` 안에 가두어 도메인 레이어로 새지 않게 한다.

```
OhMyAgent.AiAgent.Server/
├── cmd/
│   └── server/
│       └── main.go                       # 엔트리포인트: DI 조립, Gin 기동
├── internal/
│   ├── domain/                           # ── 도메인 엔티티 (순수 Go) ──
│   │   ├── llm_provider.go               # LLMProvider, ProviderType, Config
│   │   └── errors.go                     # 도메인 에러 (ErrProviderNotFound 등)
│   │
│   ├── port/                             # ── 포트 인터페이스 (계약) ──
│   │   ├── llm_repository.go             # LLMRepository (driven port)
│   │   ├── llm_adapter.go                # LLMAdapter (driven port - LLM 호출)
│   │   ├── llm_factory.go                # LLMFactory (driven port - 어댑터 생성)
│   │   └── cache.go                      # ProviderCache (driven port)
│   │
│   ├── application/                      # ── 유스케이스 (도메인 서비스) ──
│   │   ├── llm_service.go                # 활성 Provider 조회 + LLM 호출 조율
│   │   └── admin_service.go              # 활성화 전환 + 캐시 무효화
│   │
│   ├── adapter/                          # ── 어댑터 (드라이빙/드리븐 구현체) ──
│   │   ├── db/
│   │   │   ├── sqlc/                     # sqlc 생성 코드 (수정 금지)
│   │   │   │   ├── db.go
│   │   │   │   ├── models.go
│   │   │   │   └── query.sql.go
│   │   │   ├── llm_repository.go         # sqlc → 도메인 엔티티 매핑 어댑터
│   │   │   └── conn.go                   # *sql.DB 생성, 풀 설정
│   │   │
│   │   ├── llm/
│   │   │   ├── factory.go                # LLMFactory 구현 (provider_type 분기)
│   │   │   ├── ollama_adapter.go         # LOCAL: Ollama HTTP 호출
│   │   │   ├── claude_adapter.go         # EXTERNAL: Anthropic API 호출
│   │   │   └── gpt_adapter.go            # EXTERNAL: OpenAI API 호출
│   │   │
│   │   └── cache/
│   │       └── atomic_cache.go           # sync/atomic.Value 기반 캐시
│   │
│   ├── handler/                          # ── 드라이빙 어댑터 (HTTP) ──
│   │   ├── admin_handler.go              # POST /admin/providers/:id/activate
│   │   ├── llm_handler.go                # POST /v1/chat/completions
│   │   ├── dto.go                        # 요청/응답 DTO + validate 태그
│   │   └── error_response.go             # 도메인 에러 → HTTP 매핑
│   │
│   ├── infrastructure/                   # ── 횡단 관심사 ──
│   │   ├── config/config.go              # viper 기반 설정 로더
│   │   ├── logger/logger.go              # zap 초기화
│   │   └── server/router.go              # Gin 라우터 + 미들웨어 등록
│   │
│   └── middleware/
│       ├── recovery.go
│       └── request_id.go
│
├── db/                                   # ── sqlc 입력 ──
│   ├── schema.sql                        # 테이블 DDL
│   ├── query.sql                         # 쿼리 정의 (sqlc 입력)
│   └── migrations/
│       ├── 000001_create_llm_providers.up.sql
│       └── 000001_create_llm_providers.down.sql
│
├── sqlc.yaml                             # sqlc 설정
├── go.mod
└── go.sum
```

**아키텍처 의존 방향 (Outside → Inside):**
```
handler ─┐
         ├─→ application ─→ port ←─ adapter (db / llm / cache)
cmd  ────┘                     ↑
                            domain
```
`domain` 과 `port` 는 외부 라이브러리 import 금지(순수 Go). `application` 은 `port` 인터페이스에만 의존한다.

---

## 도메인 엔티티

`internal/domain/llm_provider.go` — sqlc 생성 모델과 분리된 순수 도메인 엔티티.

```go
package domain

import (
	"encoding/json"
	"time"
)

// ProviderType 은 LLM 제공자의 배포 유형을 나타낸다.
type ProviderType string

const (
	ProviderTypeLocal    ProviderType = "LOCAL"    // 사내/로컬 (예: Ollama)
	ProviderTypeExternal ProviderType = "EXTERNAL" // 외부 SaaS (Claude, GPT)
)

func (p ProviderType) Valid() bool {
	return p == ProviderTypeLocal || p == ProviderTypeExternal
}

// ProviderConfig 는 config_json 에 저장되는 가변 설정의 도메인 표현.
// JSON 의 추가 필드는 Extra 에 보관되어 어댑터별로 활용된다.
type ProviderConfig struct {
	Endpoint    string                 `json:"endpoint"`              // 예: https://api.openai.com/v1
	Model       string                 `json:"model"`                 // 예: gpt-4o, llama3.1:8b
	APIKeyEnv   string                 `json:"api_key_env,omitempty"` // 환경변수 이름 (시크릿 직접 저장 금지)
	TimeoutSec  int                    `json:"timeout_sec,omitempty"`
	MaxTokens   int                    `json:"max_tokens,omitempty"`
	Temperature float32                `json:"temperature,omitempty"`
	Extra       map[string]any         `json:"extra,omitempty"`
}

// LLMProvider 는 도메인 애그리거트 루트.
// sqlc 의 LlmProvider 와는 완전히 분리되어 있으며, 어댑터에서 매핑된다.
type LLMProvider struct {
	ID        int64
	Name      string
	IsActive  bool
	Type      ProviderType
	Config    ProviderConfig
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewLLMProvider 는 유효성 검증을 거쳐 도메인 엔티티를 생성한다.
func NewLLMProvider(name string, t ProviderType, cfg ProviderConfig) (*LLMProvider, error) {
	if name == "" {
		return nil, ErrInvalidProviderName
	}
	if !t.Valid() {
		return nil, ErrInvalidProviderType
	}
	if cfg.Model == "" {
		return nil, ErrInvalidProviderConfig
	}
	return &LLMProvider{
		Name:   name,
		Type:   t,
		Config: cfg,
	}, nil
}

// MarshalConfig 는 영속화를 위해 ProviderConfig 를 JSON 바이트로 직렬화한다.
func (p *LLMProvider) MarshalConfig() ([]byte, error) {
	return json.Marshal(p.Config)
}

// UnmarshalConfig 는 DB JSON 컬럼을 ProviderConfig 로 역직렬화한다.
func UnmarshalConfig(raw []byte) (ProviderConfig, error) {
	var c ProviderConfig
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, ErrInvalidProviderConfig
	}
	return c, nil
}
```

`internal/domain/errors.go`:

```go
package domain

import "errors"

var (
	ErrProviderNotFound      = errors.New("llm provider not found")
	ErrNoActiveProvider      = errors.New("no active llm provider configured")
	ErrInvalidProviderName   = errors.New("invalid provider name")
	ErrInvalidProviderType   = errors.New("invalid provider type")
	ErrInvalidProviderConfig = errors.New("invalid provider config")
	ErrUnsupportedProvider   = errors.New("unsupported provider type for adapter creation")
)
```

---

## 포트 인터페이스 (Ports)

모든 포트는 **도메인 엔티티만 입출력**한다. sqlc 모델, *sql.DB, *http.Request 등 인프라 타입이 시그니처에 노출되어서는 안 된다.

`internal/port/llm_repository.go` — Driven Port (DB):

```go
package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// LLMRepository 는 LLM Provider 메타데이터의 영속화 계약이다.
// 구현체는 internal/adapter/db 에 위치하며, sqlc 생성 코드를 도메인 엔티티로 매핑한다.
type LLMRepository interface {
	// GetActiveProvider 는 is_active=1 인 Provider 를 반환한다.
	// 활성 Provider 가 없으면 domain.ErrNoActiveProvider 를 반환한다.
	GetActiveProvider(ctx context.Context) (*domain.LLMProvider, error)

	// GetProviderByID 는 단일 Provider 를 조회한다.
	GetProviderByID(ctx context.Context, id int64) (*domain.LLMProvider, error)

	// ListProviders 는 전체 Provider 목록을 반환한다 (관리자 화면용).
	ListProviders(ctx context.Context) ([]*domain.LLMProvider, error)

	// ActivateProvider 는 트랜잭션으로 모든 Provider 의 is_active 를 0 으로
	// 만든 후, 지정된 id 의 Provider 만 1 로 설정한다.
	// 대상 id 가 존재하지 않으면 domain.ErrProviderNotFound 를 반환한다.
	ActivateProvider(ctx context.Context, id int64) error

	// CreateProvider / UpdateProvider 는 관리자 전용. (선택적 확장)
	CreateProvider(ctx context.Context, p *domain.LLMProvider) (int64, error)
}
```

`internal/port/llm_adapter.go` — Driven Port (LLM 호출):

```go
package port

import (
	"context"

	"OhMyAgent.AiAgent.Server/internal/domain"
)

// ChatMessage 는 LLM 호출의 단일 턴.
type ChatMessage struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// ChatRequest 는 도메인 표현의 LLM 호출 요청.
type ChatRequest struct {
	Messages    []ChatMessage
	MaxTokens   int
	Temperature float32
}

// ChatResponse 는 LLM 호출 결과.
type ChatResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}

// LLMAdapter 는 특정 LLM 벤더(Ollama / Claude / GPT) 한 인스턴스를 추상화한다.
// 어댑터는 무상태(stateless) 이며 활성 Provider 가 바뀌면 새 인스턴스로 교체된다.
type LLMAdapter interface {
	// Chat 은 동기 호출. 스트리밍은 별도 메서드로 확장 가능.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// Provider 는 이 어댑터를 만든 도메인 Provider 를 반환 (관측/디버깅).
	Provider() *domain.LLMProvider
}

// LLMFactory 는 도메인 Provider 정보로부터 LLMAdapter 를 인스턴스화한다.
// provider_type 과 config_json 을 보고 OllamaAdapter / ClaudeAdapter / GPTAdapter 중 하나를 반환.
type LLMFactory interface {
	Build(p *domain.LLMProvider) (LLMAdapter, error)
}
```

`internal/port/cache.go` — Driven Port (캐시):

```go
package port

import "OhMyAgent.AiAgent.Server/internal/domain"

// ProviderCache 는 활성 LLMAdapter 를 lock-free 로 보관한다.
// 구현체는 sync/atomic.Value 기반.
type ProviderCache interface {
	// Load 는 현재 활성 어댑터를 반환. 없으면 (nil, false).
	Load() (LLMAdapter, bool)

	// Store 는 새 어댑터로 원자적 교체.
	Store(a LLMAdapter)

	// Invalidate 는 캐시를 비운다 (다음 Load 호출 시 재로딩 필요).
	Invalidate()
}
```

**의존 흐름 요약:**
- `application.LLMService` 는 `ProviderCache.Load` → 캐시 미스 시 `LLMRepository.GetActiveProvider` → `LLMFactory.Build` → `ProviderCache.Store` 순으로 동작.
- `application.AdminService.Activate(id)` 는 `LLMRepository.ActivateProvider` → `ProviderCache.Invalidate` 를 호출하여 다음 요청부터 새 어댑터가 로드되도록 한다.

---

## sqlc 파일 계획

### `db/schema.sql` (sqlc 가 타입 추론에 사용. 마이그레이션은 별도 파일과 동일 내용 유지)

```sql
-- LLM 제공자 메타데이터.
-- is_active=1 인 행은 항상 정확히 0개 또는 1개여야 한다 (애플리케이션 트랜잭션으로 보장).
CREATE TABLE IF NOT EXISTS llm_providers (
    id            BIGINT       NOT NULL AUTO_INCREMENT,
    name          VARCHAR(100) NOT NULL,
    is_active     TINYINT(1)   NOT NULL DEFAULT 0,
    provider_type ENUM('LOCAL', 'EXTERNAL') NOT NULL,
    config_json   JSON         NOT NULL,
    created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_llm_providers_name (name),
    KEY idx_llm_providers_is_active (is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

### `db/query.sql` (sqlc 입력)

```sql
-- name: GetActiveProvider :one
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
WHERE is_active = 1
LIMIT 1;

-- name: GetProviderByID :one
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
WHERE id = ?;

-- name: ListProviders :many
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
ORDER BY id ASC;

-- name: DeactivateAllProviders :exec
UPDATE llm_providers SET is_active = 0 WHERE is_active = 1;

-- name: ActivateProviderByID :execrows
UPDATE llm_providers SET is_active = 1 WHERE id = ?;

-- name: CreateProvider :execresult
INSERT INTO llm_providers (name, is_active, provider_type, config_json)
VALUES (?, ?, ?, ?);

-- ActivateProvider 는 어댑터 측 Go 코드에서 트랜잭션으로 묶는다:
--   BEGIN
--     DeactivateAllProviders
--     ActivateProviderByID(id)  -- 영향받은 행이 0이면 롤백 + ErrProviderNotFound
--   COMMIT
```

### `sqlc.yaml`

```yaml
version: "2"
sql:
  - engine: "mysql"
    queries: "db/query.sql"
    schema:  "db/schema.sql"
    gen:
      go:
        package: "sqlc"
        out: "internal/adapter/db/sqlc"
        sql_package: "database/sql"
        emit_json_tags: false
        emit_prepared_queries: false
        emit_interface: true
        emit_exact_table_names: false
        emit_empty_slices: true
        overrides:
          - column: "llm_providers.config_json"
            go_type: "[]byte"
          - column: "llm_providers.is_active"
            go_type:
              type: "bool"
```

> 참고: `is_active` 는 MariaDB 의 `TINYINT(1)` 이므로 sqlc override 로 Go 의 `bool` 로 매핑한다.
> `config_json` 은 `[]byte` 로 받아 어댑터 레이어에서 `domain.UnmarshalConfig` 로 변환한다.

### `db/migrations/000001_create_llm_providers.up.sql`
`schema.sql` 과 동일 DDL.

### `db/migrations/000001_create_llm_providers.down.sql`

```sql
DROP TABLE IF EXISTS llm_providers;
```

---

## 의존성

```bash
# DB 스택
go get github.com/go-sql-driver/mysql@v1.8.1
go get github.com/jmoiron/sqlx@v1.4.0           # (선택) 트랜잭션 헬퍼용. 미사용 시 제외 가능
go get github.com/golang-migrate/migrate/v4@v4.17.1

# 설정/관측
go get github.com/spf13/viper@v1.18.2
# zap, uuid, gin 은 이미 go.mod 에 존재

# 테스트
go get github.com/stretchr/testify@v1.9.0
go get github.com/DATA-DOG/go-sqlmock@v1.5.2

# sqlc CLI (호스트 도구, go.mod 와 무관)
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.25.0
```

---

## Admin API 엔드포인트 설계

| 메서드 | 경로 | 설명 | 요청 본문 | 응답 |
|--------|------|------|-----------|------|
| GET    | `/admin/providers`               | 전체 Provider 목록 조회 | — | `200` ProviderList |
| GET    | `/admin/providers/:id`           | 단일 Provider 조회 | — | `200` Provider / `404` |
| POST   | `/admin/providers`               | Provider 신규 등록 | CreateProviderRequest | `201` Provider |
| POST   | `/admin/providers/:id/activate`  | 활성 Provider 전환 (캐시 무효화 트리거) | — | `200 {"activated_id": id}` / `404` |
| GET    | `/admin/providers/active`        | 현재 활성 Provider + 어댑터 헬스 | — | `200` ActiveProviderStatus |
| POST   | `/v1/chat/completions`           | LLM 호출 (활성 Provider 사용) | ChatRequest | `200` ChatResponse |
| GET    | `/healthz`                       | 라이브니스 | — | `200 {"status":"ok"}` |
| GET    | `/readyz`                        | DB 연결 + 활성 Provider 존재 검사 | — | `200` / `503` |

핸들러 스택: `recovery → request_id → zap_logger → cors → routes`. Admin 라우트는 추후 인증 미들웨어(예: API key) 추가를 전제.

---

## 캐싱 전략

**핵심 아이디어:** 활성 LLMAdapter 인스턴스 자체를 `sync/atomic.Value` 에 보관하여 모든 요청 경로에서 lock-free 로 읽는다. 변경은 관리자 액션이 발생할 때만 일어나며, 이때만 캐시를 무효화한다.

### 자료구조
```go
// internal/adapter/cache/atomic_cache.go
type AtomicProviderCache struct {
    v atomic.Value // *cacheEntry (nil 또는 어댑터 보관)
}

type cacheEntry struct {
    adapter port.LLMAdapter
}
```

### 동작 시퀀스

1. **읽기 (LLMService.Chat)**
   - `cache.Load()` 호출 → 어댑터 있으면 즉시 사용.
   - 캐시 미스 시 single-flight (`golang.org/x/sync/singleflight` 또는 sync.Mutex) 로
     `repo.GetActiveProvider` → `factory.Build` → `cache.Store` 후 사용.

2. **쓰기 (AdminService.Activate)**
   - `repo.ActivateProvider(id)` 트랜잭션 성공 시 `cache.Invalidate()` 호출.
   - 다음 LLM 요청에서 자연스럽게 재로딩 (lazy reload).

3. **부팅**
   - `main.go` 에서 비동기로 prewarm: 활성 Provider 조회 → 어댑터 생성 → 캐시 저장.
     실패는 로깅만, 첫 요청 시 lazy load 로 복구.

### 동시성 보장
- `atomic.Value.Store/Load` 자체가 메모리 모델상 happens-before 관계를 제공하므로 락 없이 안전.
- Invalidate 후 동시에 여러 요청이 미스를 일으키는 상황은 singleflight 로 단 1회의 `factory.Build` 만 수행하도록 묶는다 (외부 LLM 초기화 부하 절감).

### 캐시 무효화 주체
- **포함:** Activate API, Provider 설정 업데이트 API.
- **제외:** 일반 LLM 요청 경로에서는 캐시를 변경하지 않는다 (CQRS 와 유사한 분리).

---

## 에러 처리 전략

### 도메인 에러 (sentinel + Wrap)
`internal/domain/errors.go` 의 sentinel 에러를 어댑터/서비스에서 `fmt.Errorf("...: %w", domain.ErrXxx)` 로 래핑하여 컨텍스트를 누적한다. 핸들러는 `errors.Is` 로 분기.

### 핸들러 매핑 (`internal/handler/error_response.go`)

| 도메인 에러 | HTTP 상태 | 코드 |
|-------------|-----------|------|
| `domain.ErrProviderNotFound` | 404 | `PROVIDER_NOT_FOUND` |
| `domain.ErrNoActiveProvider` | 503 | `NO_ACTIVE_PROVIDER` |
| `domain.ErrInvalidProviderName` / `ErrInvalidProviderType` / `ErrInvalidProviderConfig` | 400 | `INVALID_PROVIDER_PAYLOAD` |
| `domain.ErrUnsupportedProvider` | 500 | `UNSUPPORTED_PROVIDER` |
| 그 외 (DB / LLM 호출 실패) | 502 또는 500 | `UPSTREAM_ERROR` / `INTERNAL_ERROR` |
| Validator 실패 | 400 | `VALIDATION_ERROR` (필드 상세 동봉) |

### 응답 본문 포맷
```json
{
  "error": {
    "code": "PROVIDER_NOT_FOUND",
    "message": "llm provider with id=42 not found",
    "request_id": "01HX...",
    "details": []
  }
}
```

### LLM 어댑터 에러
- 외부 호출은 `context.WithTimeout` 으로 보호.
- HTTP 4xx / 5xx 는 어댑터 내부에서 `fmt.Errorf("ollama: status %d: %s", code, body)` 로 래핑하여 서비스로 전달.
- 인증 실패(401/403)는 별도 sentinel `ErrLLMUnauthorized` 로 구분하여 운영 알림 분기를 가능케 함.

---

## 가정사항

1. **단일 활성 Provider:** 비즈니스 규칙상 동시에 활성화되는 Provider 는 0 또는 1 개. 멀티 활성(예: A/B) 이 필요해지면 `is_active` → `priority INT` 또는 별도 라우팅 정책 테이블로 확장.
2. **시크릿 보관:** API 키는 DB `config_json` 에 평문 저장하지 않는다. `api_key_env` 에 환경변수 이름만 저장하고, 어댑터는 런타임에 해당 env 를 읽는다 (Vault/KMS 도입 전 잠정안).
3. **단일 노드 캐시:** in-memory 캐시이므로 멀티 인스턴스 배포 시 노드별로 상이한 캐시 상태가 잠시 존재할 수 있다. 무효화 전파가 필요해지면 Redis Pub/Sub 기반 invalidation bus 도입.
4. **MariaDB 호환:** sqlc engine 은 `mysql` 사용 (MariaDB 와 와이어 호환). MariaDB 전용 함수는 사용하지 않는다.
5. **트랜잭션 처리:** sqlc 는 트랜잭션 자체는 생성하지 않으므로, `ActivateProvider` 는 어댑터 코드에서 `db.BeginTx → q := sqlc.New(tx)` 패턴으로 묶는다.
6. **스트리밍 미지원 (v1):** SSE/스트리밍 응답은 후속 마일스톤. 현 설계는 동기 chat completions 만 다룬다.
7. **인증 후행:** Admin API 인증은 본 단계에서 미들웨어 인터페이스 자리만 마련하고 구체 구현은 별도 작업으로 분리.
8. **관측성:** zap 구조화 로그 + request_id 미들웨어. 메트릭(prometheus) / 트레이싱(otel) 은 후속 단계.
9. **테스트:** Repository 는 go-sqlmock, 핸들러는 httptest, 어댑터는 인터페이스 모킹으로 단위 테스트. 통합 테스트는 docker-compose 로 MariaDB 띄워 별도 빌드 태그(`//go:build integration`)로 분리.
