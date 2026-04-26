# 데이터 레이어 구현 요약

본 문서는 LLM 제공자 동적 스위칭 시스템의 **데이터 레이어** 구현을 정리한다.
헥사고널 아키텍처의 **도메인 + 포트 + DB 어댑터** 까지 완료했으며,
다음 단계인 핸들러/서비스 레이어 구현(implementer)에 필요한 계약과 주의사항을 함께 명시한다.

모듈명: `OhMyAgent.AiAgent.Server` (go.mod 확인 완료)

---

## 1. 구현된 파일 목록

### A. DB / sqlc 입력
| 경로 | 역할 |
|------|------|
| `db/schema.sql` | MariaDB 호환 `llm_providers` DDL (sqlc 타입 추론용) |
| `db/query.sql` | sqlc 입력 쿼리 8종 (`GetActiveProvider`, `GetProviderByID`, `ListProviders`, `CreateProvider`, `DeactivateAllProviders`, `ActivateProviderByID`, `UpdateProviderConfig`, `DeleteProvider`) |
| `sqlc.yaml` | sqlc 설정 (engine: mysql, package: `sqlcdb`, out: `internal/adapter/db/sqlc`, override: `tinyint(1)`→`bool`, `json`→`encoding/json.RawMessage`) |

### B. 도메인 엔티티
| 경로 | 역할 |
|------|------|
| `internal/domain/llm_provider.go` | `ProviderType` (LOCAL/EXTERNAL 상수), `ProviderConfig`, `LLMProvider` 애그리거트, `Validate()` |
| `internal/domain/errors.go` | sentinel 에러: `ErrProviderNotFound`, `ErrNoActiveProvider`, `ErrInvalidProvider`, `ErrInvalidProviderType`, `ErrProviderConflict` |

### C. 포트 인터페이스
| 경로 | 역할 |
|------|------|
| `internal/port/llm_repository.go` | `LLMRepository` 인터페이스 (DB driven port) |
| `internal/port/llm_adapter.go` | `LLMAdapter`, `LLMFactory`, `ProviderCache` 인터페이스 (LLM driven port + 캐시 port) |

### D. sqlc 생성 코드 시뮬레이션 (`internal/adapter/db/sqlc/`, package `sqlcdb`)
| 경로 | 역할 |
|------|------|
| `internal/adapter/db/sqlc/db.go` | `DBTX` 인터페이스, `Queries` 구조체, `New()`, `WithTx()` |
| `internal/adapter/db/sqlc/models.go` | `LlmProvider` 구조체, `LlmProvidersProviderType` ENUM 상수 + `Scan()` |
| `internal/adapter/db/sqlc/query.sql.go` | 쿼리 함수 + `CreateProviderParams` / `UpdateProviderConfigParams` |

### E. DB 어댑터 (port 구현체)
| 경로 | 역할 |
|------|------|
| `internal/adapter/db/llm_provider_repository.go` | `LLMProviderRepository` (port.LLMRepository 구현) + 매핑 헬퍼 |

### F. 공용 에러
| 경로 | 역할 |
|------|------|
| `pkg/apperror/error.go` | 공용 sentinel: `ErrNotFound`, `ErrUnauthorized`, `ErrConflict`, `ErrForbidden`, `ErrBadRequest` |

### G. 마이그레이션 (golang-migrate 호환)
| 경로 | 역할 |
|------|------|
| `migrations/001_create_llm_providers.up.sql` | `db/schema.sql` 과 동일 DDL |
| `migrations/001_create_llm_providers.down.sql` | `DROP TABLE IF EXISTS llm_providers;` |

---

## 2. 인터페이스 메서드 목록

### `port.LLMRepository`
| 메서드 | 시그니처 | 비고 |
|--------|---------|------|
| `GetActiveProvider` | `(ctx) (*domain.LLMProvider, error)` | 미존재 시 `domain.ErrNoActiveProvider` |
| `GetProviderByID` | `(ctx, id int64) (*domain.LLMProvider, error)` | 미존재 시 `domain.ErrProviderNotFound` |
| `ListProviders` | `(ctx) ([]*domain.LLMProvider, error)` | id ASC |
| `CreateProvider` | `(ctx, *domain.LLMProvider) (*domain.LLMProvider, error)` | 자동 ID/타임스탬프 채워 반환 |
| `ActivateProvider` | `(ctx, id int64) error` | **트랜잭션** (DeactivateAll → ActivateByID) / 미존재 시 ErrProviderNotFound |
| `UpdateProviderConfig` | `(ctx, id int64, config domain.ProviderConfig) error` | 미존재 시 ErrProviderNotFound |
| `DeleteProvider` | `(ctx, id int64) error` | 미존재 시 ErrProviderNotFound |

### `port.LLMAdapter`
| 메서드 | 시그니처 |
|--------|---------|
| `Complete` | `(ctx, prompt string) (string, error)` |
| `ProviderType` | `() domain.ProviderType` |

### `port.LLMFactory`
| 메서드 | 시그니처 |
|--------|---------|
| `CreateAdapter` | `(provider *domain.LLMProvider) (port.LLMAdapter, error)` |

### `port.ProviderCache`
| 메서드 | 시그니처 |
|--------|---------|
| `Get` | `() *domain.LLMProvider` |
| `Set` | `(provider *domain.LLMProvider)` |
| `Invalidate` | `()` |

---

## 3. sqlc 타입 오버라이드 설명

`sqlc.yaml` 의 `overrides` 항목:

| DB 타입 | Go 타입 | 이유 |
|---------|--------|------|
| `tinyint(1)` | `bool` | MariaDB 의 boolean 표현. Go 도메인은 `bool` 로 다루는 것이 자연스럽다. |
| `json` | `encoding/json.RawMessage` | DB 의 JSON 컬럼을 평문 바이트로 받아 어댑터 레이어에서 `domain.ProviderConfig` 로 디코드. 도메인이 `json` 패키지에 의존하지 않도록 분리. |

ENUM(`provider_type`) 은 sqlc 가 자동으로 패키지 내부 타입 `LlmProvidersProviderType` 과 상수 `LlmProvidersProviderType{LOCAL,EXTERNAL}` 로 매핑한다. 어댑터에서 `domain.ProviderType` 와 양방향 변환된다 (`toDomainProviderType` / `toSQLProviderType`).

---

## 4. 트랜잭션 처리 방법

`ActivateProvider(id)` 는 **두 SQL 을 단일 트랜잭션** 으로 묶어 원자성을 보장한다:

```go
tx, _ := r.db.BeginTx(ctx, nil)
defer tx.Rollback()           // 안전망 - 에러 경로에서 자동 롤백
qtx := r.queries.WithTx(tx)   // sqlc 가 트랜잭션 위에서 동작하도록 분기

qtx.DeactivateAllProviders(ctx)              // is_active=1 인 모든 행을 0으로
affected, _ := qtx.ActivateProviderByID(ctx, id)
if affected == 0 {
    return domain.ErrProviderNotFound        // 롤백되며 변경사항 없음
}
tx.Commit()
```

핵심 포인트:
- `*sql.DB` 자체를 레포지토리에 보관하는 이유는 트랜잭션을 직접 열어야 하기 때문이다.
- `Queries.WithTx(tx)` 패턴으로 동일 메서드 셋을 트랜잭션 위에서 재사용한다.
- 영향 행이 0인 경우 트랜잭션을 커밋하지 않고 도메인 에러를 반환 → 모든 변경 자동 롤백.
- `ActivateProviderByID` 는 sqlc `:execrows` 로 RowsAffected 를 반환하도록 구성.

---

## 5. 도메인-어댑터 분리 원칙

**의존 방향: domain ← port ← adapter**

| 레이어 | import 가능 | import 금지 |
|--------|-----------|-----------|
| `internal/domain` | 표준 라이브러리만 | sqlc, sql, gin, json 외 (현재 time 만 사용) |
| `internal/port` | `internal/domain` | adapter, sqlc, infra |
| `internal/adapter/db/sqlc` | 표준 라이브러리만 (`database/sql`, `encoding/json`, `time`) | **domain, port 절대 금지** |
| `internal/adapter/db/llm_provider_repository.go` | domain, port, sqlc | handler, application |

매핑은 **오직** `internal/adapter/db/llm_provider_repository.go` 의 `toDomainProvider`, `toDomainProviderType`, `toSQLProviderType`, `unmarshalConfig` 헬퍼에서만 수행한다.

---

## 6. implementer 에이전트를 위한 주의사항

1. **레포지토리 생성자 시그니처**
   ```go
   db.NewLLMProviderRepository(sqlDB *sql.DB) *db.LLMProviderRepository
   ```
   `sql.DB` 는 `internal/adapter/db/conn.go`(미구현, implementer 가 작성) 에서 풀 설정과 함께 만들어 DI 컨테이너에 등록할 것. `internal/adapter/db` 패키지명은 `db` 이다.

2. **에러 분기 패턴**
   서비스/핸들러는 `errors.Is(err, domain.ErrXxx)` 로 분기한다. 어댑터는 `fmt.Errorf("...: %w", err)` 로 래핑하므로 직접 비교 (`==`) 는 사용 금지.
   - `domain.ErrNoActiveProvider` → HTTP 503
   - `domain.ErrProviderNotFound` → HTTP 404
   - `domain.ErrInvalidProvider*` → HTTP 400
   - 그 외 → HTTP 500

3. **ProviderCache 의 Get/Set 시그니처 변경 주의**
   설계 문서(01) 의 `Load/Store/(LLMAdapter, bool)` 와 달리, 본 구현은 사용자 사양에 따라 **도메인 엔티티** 를 캐싱한다 (`Get() *domain.LLMProvider`, `Set(*domain.LLMProvider)`). 따라서 어댑터 인스턴스는 실제 LLM 호출 직전에 `LLMFactory.CreateAdapter` 로 생성해야 한다. 캐시는 도메인 엔티티만 보관하므로 어댑터 빌드 비용을 줄이려면 implementer 가 별도로 sync.Map 또는 atomic.Value 기반 어댑터 캐시를 추가할 것 (선택).

4. **CreateProvider 호출 시 IsActive**
   `CreateProvider` 는 입력 그대로 INSERT 한다. 새 Provider 를 즉시 활성화하려면 별도로 `ActivateProvider(id)` 를 호출해야 한다 (관리 화면 UX 일관성).

5. **config_json 보안**
   `domain.ProviderConfig.APIKeyEnv` 는 환경변수 **이름** 만 저장한다. LLM 어댑터 구현체에서 `os.Getenv(cfg.APIKeyEnv)` 로 시크릿을 읽어들일 것. DB 에 시크릿 평문이 저장되지 않도록 핸들러 DTO 에서도 검증할 것.

6. **sqlc 재생성 시 주의**
   `internal/adapter/db/sqlc/` 는 현재 수동 작성된 sqlc 등가 코드이다. 실제 `sqlc generate` 로 재생성하면:
   - `CreateProvider` 시그니처가 `(sql.Result, error)` 로 바뀐다 (`:execresult`). 매핑 어댑터의 `CreateProvider` 도 `LastInsertId()` 후 `GetProviderByID` 호출 패턴으로 보정해야 한다 (현재 동작과 동일).
   - `ActivateProviderByID` / `DeleteProvider` / `UpdateProviderConfig` 는 `:execrows` 시그니처 `(int64, error)` 가 그대로 유지된다.
   - 함수 분포는 동일하게 유지되도록 query.sql 의 `-- name:` 주석을 보존했다.

7. **의존성 추가**
   implementer 단계에서 다음을 `go get` 으로 추가해야 한다:
   - `github.com/go-sql-driver/mysql` (DB 드라이버)
   - `github.com/golang-migrate/migrate/v4` 및 mysql 드라이버 (선택, 마이그레이션 자동 적용 시)
   - `github.com/spf13/viper` (config 로딩)
   - 테스트: `github.com/stretchr/testify`, `github.com/DATA-DOG/go-sqlmock`

8. **트랜잭션 헬퍼 노출 여부**
   `Queries.WithTx(*sql.Tx) *Queries` 는 어댑터 패키지 외부로 노출되어 있지만, 도메인/포트는 알지 못한다. 다른 트랜잭션 묶음이 필요하면 어댑터 메서드를 새로 추가하는 식으로 캡슐화할 것.

9. **기존 코드와의 충돌 회피**
   기존 프로젝트에 `internal/domain/port/` 패키지가 존재한다 (다른 도메인용 inbound/outbound). 본 작업은 별도 경로 `internal/port/` 에 LLM Provider 전용 포트를 추가했다 — 이 둘은 서로 다른 책임을 가지며 import 경로로 명확히 분리된다.

---

## 7. 검증 체크리스트 (implementer 가 확인할 것)

- [ ] `go build ./...` 가 성공하는가?
- [ ] `go vet ./...` 통과?
- [ ] sqlc 재생성 시 `internal/adapter/db/sqlc/` 의 함수 시그니처가 어댑터 매핑과 호환되는가?
- [ ] golang-migrate 로 `migrations/001_create_llm_providers.up.sql` 적용 후 `db/schema.sql` 과 동일한 스키마가 만들어지는가?
- [ ] `port.LLMRepository` 가 어댑터로 충족됨 (`var _ port.LLMRepository = (*db.LLMProviderRepository)(nil)` 컴파일 통과)?
