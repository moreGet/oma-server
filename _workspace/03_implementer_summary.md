# HTTP 레이어 / 비즈니스 로직 구현 요약

본 문서는 LLM 제공자 동적 스위칭 시스템의 **HTTP 레이어 + Application 서비스 + 캐시/팩토리/LLM 어댑터 + DI 조립** 구현을 정리한다.
다음 단계인 **테스트 레이어(qa-engineer)** 가 참조해야 할 표면(Surface), 캐시 흐름, 분기 로직, 검증 포인트를 명시한다.

모듈명: `OhMyAgent.AiAgent.Server`

---

## 1. 구현된 파일 목록

### A. 캐시 어댑터
| 경로 | 역할 |
|------|------|
| `internal/adapter/cache/provider_cache.go` | `port.ProviderCache` 구현. `sync/atomic.Value` 기반 lock-free 캐시. 생성 시 typed nil 적재로 `Invalidate()` 의 panic 회피 |

### B. LLM 어댑터 (외부 호출 - 현재 모두 스텁)
| 경로 | 역할 |
|------|------|
| `internal/adapter/llm/ollama_adapter.go` | LOCAL 용 (`OllamaAdapter`). `ProviderType()` → LOCAL |
| `internal/adapter/llm/claude_adapter.go` | EXTERNAL 용 (`ClaudeAdapter`). `os.Getenv(config.APIKeyEnv)` 로 시크릿 로드 |
| `internal/adapter/llm/openai_adapter.go` | EXTERNAL 용 (`OpenAIAdapter`). `os.Getenv(config.APIKeyEnv)` 로 시크릿 로드 |
| `internal/adapter/llm/factory.go` | `port.LLMFactory` 구현. provider_type + model 명으로 어댑터 분기 |

### C. Application 서비스
| 경로 | 역할 |
|------|------|
| `internal/application/llm_provider_service.go` | `LLMProviderService` — Repository + Cache + Factory 조율. CRUD + GetActiveLLMAdapter + 캐시 무효화 |

### D. DTO
| 경로 | 역할 |
|------|------|
| `internal/dto/llm_provider.go` | `CreateProviderRequest`, `UpdateConfigRequest`, `ProviderConfigDTO`, `ProviderResponse`, `ErrorResponse`, `MessageResponse` |

### E. HTTP 핸들러
| 경로 | 역할 |
|------|------|
| `internal/handler/llm_provider_handler.go` | `LLMProviderHandler` 6개 엔드포인트 + DTO/도메인 매핑 헬퍼 + 에러 → HTTP 매핑 헬퍼 (`respondError`, `parseIDParam`) |

### F. 미들웨어
| 경로 | 역할 |
|------|------|
| `internal/middleware/recovery.go` | `gin.CustomRecovery` 기반 패닉 → 500 + zap 로깅 |
| `internal/middleware/logger.go` | zap 구조화 로깅 (method/path/status/latency/client_ip) |
| `internal/middleware/cors.go` | 자작 CORS 미들웨어 (gin-contrib/cors 미설치 환경 대응). preflight 처리 포함 |

### G. 라우터
| 경로 | 역할 |
|------|------|
| `internal/router/router.go` | Gin 엔진 구성. 미들웨어 순서: Recovery → Logger → CORS. `/health` + `/api/v1/admin/llm-providers/*` 라우트 등록 |

### H. 설정 / 인프라
| 경로 | 역할 |
|------|------|
| `internal/config/config.go` | env 기반 설정 로더 (DATABASE_URL, SERVER_PORT, GIN_MODE, DB_MAX_CONNS) |
| `internal/infrastructure/db.go` | `NewMariaDB(cfg)` — `*sql.DB` 풀 생성 + Ping 검증. MariaDB 와이어 호환 mysql 드라이버 사용 |

### I. 엔트리포인트
| 경로 | 역할 |
|------|------|
| `cmd/server/main.go` | DI 조립 + 서버 기동 (LLM Provider 시스템). 기존 agentic loop 통합은 다음 마일스톤으로 분리 |

### J. 의존성
| 항목 | 변경 |
|------|------|
| `go.mod` | `github.com/go-sql-driver/mysql v1.8.1` 추가, `filippo.io/edwards25519 v1.1.0` (mysql 의 indirect) 추가 |

---

## 2. 엔드포인트 표

| 메서드 | 경로 | 핸들러 | 설명 |
|--------|------|--------|------|
| GET    | `/health`                                       | inline                                     | 라이브니스 (`{"status":"ok"}`) |
| GET    | `/api/v1/admin/llm-providers`                   | `LLMProviderHandler.ListProviders`         | 전체 Provider 목록 (id ASC) |
| GET    | `/api/v1/admin/llm-providers/:id`               | `LLMProviderHandler.GetProvider`           | 단일 Provider 조회 |
| POST   | `/api/v1/admin/llm-providers`                   | `LLMProviderHandler.CreateProvider`        | 신규 Provider 생성 (201) |
| PUT    | `/api/v1/admin/llm-providers/:id/activate`      | `LLMProviderHandler.ActivateProvider`      | 활성 Provider 전환 + 캐시 무효화 |
| PATCH  | `/api/v1/admin/llm-providers/:id/config`        | `LLMProviderHandler.UpdateProviderConfig`  | config_json 만 갱신 (필요 시 캐시 무효화) |
| DELETE | `/api/v1/admin/llm-providers/:id`               | `LLMProviderHandler.DeleteProvider`        | Provider 삭제 (204) |

### 에러 → HTTP 매핑 (`handler.respondError`)
| 도메인 에러 | HTTP |
|-------------|------|
| `domain.ErrProviderNotFound` | 404 |
| `domain.ErrNoActiveProvider` | 404 |
| `domain.ErrInvalidProvider`  | 400 |
| `domain.ErrInvalidProviderType` | 400 |
| `domain.ErrProviderConflict` | 409 |
| 그 외 | 500 (`{"error":"internal server error","details":...}`) |

`errors.Is` 비교를 사용하므로 어댑터의 `fmt.Errorf("...: %w", domain.ErrXxx)` 래핑 체인 전체를 인식한다.

---

## 3. 캐싱 흐름

### 3.1 자료구조
`internal/adapter/cache/provider_cache.go` — `sync/atomic.Value` 한 슬롯에 `*domain.LLMProvider` 를 저장.

```
+----------------------+         atomic.Store / atomic.Load
|  providerCache       |  -----> race-free, lock-free, happens-before
|   value atomic.Value |
+----------------------+
```

`atomic.Value.Store(nil)` 은 panic 을 일으키므로, **Invalidate** 는 typed nil (`(*domain.LLMProvider)(nil)`) 을 적재한다. `Get` 은 typed nil 을 알아차리고 일반 nil 을 반환한다.

### 3.2 읽기 시퀀스 (`LLMProviderService.GetActiveLLMAdapter`)

```
Handler ─► Service.GetActiveLLMAdapter
              │
              │  cache.Get()
              ├── 캐시 hit  ──► factory.CreateAdapter(provider) ─► return adapter
              │
              └── 캐시 miss
                    │
                    ├── repo.GetActiveProvider(ctx)
                    │     ├─ row 발견 ─► provider
                    │     └─ 미발견   ─► return ErrNoActiveProvider
                    │
                    ├── cache.Set(provider)            // lock-free swap
                    └── factory.CreateAdapter(provider) ─► return adapter
```

**참고**: 본 구현은 도메인 엔티티만 캐싱하며, 어댑터 자체는 매 요청마다 `factory.CreateAdapter` 로 빌드한다. 현재는 모든 어댑터가 stub 이므로 비용이 미미하지만, 실제 구현 시 무거운 초기화가 들어간다면 어댑터 캐시(`atomic.Value` 두 번째 슬롯)를 추가하는 전략을 권고.

### 3.3 무효화 (Write 경로)

| 액션 | 무효화 조건 |
|------|-----------|
| `ActivateProvider(id)` | **항상 무효화** — 활성 대상이 바뀌었을 가능성 |
| `UpdateProviderConfig(id, cfg)` | **캐시된 Provider 의 ID == id 일 때만** 무효화 (다른 비활성 Provider 의 config 변경은 활성 어댑터에 영향 없음) |
| `DeleteProvider(id)` | **캐시된 Provider 의 ID == id 일 때만** 무효화 |
| `CreateProvider` | **무효화 안 함** — 새 Provider 는 비활성 상태로 등록되며 활성 전환은 별도 Activate API 가 담당 |

### 3.4 동시성 고려
- `atomic.Value` 의 Load/Store 는 happens-before 관계 보장. 추가 락 불필요.
- 동시에 여러 요청이 캐시 미스를 일으키는 경우, 여러 번의 `repo.GetActiveProvider` + `factory.CreateAdapter` 가 발생할 수 있음. 트래픽 폭증 환경에서는 `golang.org/x/sync/singleflight` 도입 검토 (현재 미적용 — 외부 의존성 추가 회피).

---

## 4. LLM 팩토리 분기 로직

`internal/adapter/llm/factory.go`

```
provider.ProviderType
 ├── LOCAL          ──► OllamaAdapter
 │
 └── EXTERNAL
       └── provider.Config.Model
             ├── "claude" | "claude-3" | "claude-sonnet"  ──► ClaudeAdapter
             └── 그 외 (예: "gpt-4o", "gpt-3.5-turbo")     ──► OpenAIAdapter (기본 EXTERNAL)
```

- **provider.ProviderType 이 알 수 없는 값**: `fmt.Errorf("unknown provider type: ...")` 반환 (서비스 → 핸들러에서 500 처리). 도메인 sentinel(`ErrInvalidProviderType`) 과 별도임에 주의 — 정상 흐름에서는 sqlc ENUM 으로 인해 도달 불가.
- **provider 가 nil**: `fmt.Errorf("nil provider")` 반환. 방어적 코드.

---

## 5. DTO ↔ 도메인 매핑

핸들러 파일 내 헬퍼:
- `toProviderResponse(*domain.LLMProvider) dto.ProviderResponse` — 응답 직렬화
- `fromCreateRequest(*dto.CreateProviderRequest) *domain.LLMProvider` — 생성 요청 → 도메인
- `fromConfigDTO(dto.ProviderConfigDTO) domain.ProviderConfig` — config 변환

DTO 는 `binding:"required,oneof=LOCAL EXTERNAL"` 등 Gin 의 validator 태그로 1차 검증. 도메인 `Validate()` 에서 2차 검증.

---

## 6. DI 조립 (`cmd/server/main.go`)

```
config.Load
   ↓
infrastructure.NewMariaDB (sql.Open + Ping)
   ↓
db.NewLLMProviderRepository (sqlcdb.New 내부 보유)
   ↓                                ┌─ cache.NewProviderCache
   ├──────────────────────────────► ├─ llm.NewLLMFactory
   │                                ↓
application.NewLLMProviderService(repo, cache, factory)
   ↓
handler.NewLLMProviderHandler(svc)
   ↓
router.New(handler, logger)
   ↓
gin.Engine.Run(cfg.ServerAddr())
```

리소스 정리: `defer sqlDB.Close()`, `defer logger.Sync()`.

---

## 7. qa-engineer 를 위한 테스트 포인트

### 7.1 단위 테스트 (mock 기반)

#### `internal/adapter/cache/provider_cache_test.go`
- [ ] `NewProviderCache().Get()` → nil
- [ ] `Set(p)` 후 `Get()` → 같은 포인터
- [ ] `Invalidate()` 후 `Get()` → nil (panic 없음)
- [ ] `Store(nil)` panic 회피 검증 (`Invalidate` 두 번 호출)
- [ ] 동시성 (goroutine 100개 + 100회 Set/Get 인터리빙) — race detector 활성화

#### `internal/adapter/llm/factory_test.go`
- [ ] LOCAL → `*OllamaAdapter`
- [ ] EXTERNAL + model="claude" → `*ClaudeAdapter`
- [ ] EXTERNAL + model="claude-3" → `*ClaudeAdapter`
- [ ] EXTERNAL + model="claude-sonnet" → `*ClaudeAdapter`
- [ ] EXTERNAL + model="gpt-4o" → `*OpenAIAdapter`
- [ ] EXTERNAL + model="" → `*OpenAIAdapter` (default)
- [ ] 알 수 없는 ProviderType → error
- [ ] nil provider → error

#### `internal/adapter/llm/{ollama,claude,openai}_adapter_test.go`
- [ ] `ProviderType()` 반환값
- [ ] `Complete()` 가 model 명을 응답 문자열에 포함하는지 (스텁 검증)
- [ ] Claude/OpenAI: `os.Setenv(APIKeyEnv, "x")` 후 응답에 `key=present` 포함

#### `internal/application/llm_provider_service_test.go` (mock repo + cache + factory)
- [ ] `GetActiveLLMAdapter`: 캐시 hit 경로에서 `repo.GetActiveProvider` 미호출
- [ ] `GetActiveLLMAdapter`: 캐시 miss 시 repo 호출 → `cache.Set` 호출 → factory 호출
- [ ] `GetActiveLLMAdapter`: repo 가 `ErrNoActiveProvider` 반환 시 그대로 전파
- [ ] `ActivateProvider`: repo 성공 → `cache.Invalidate` 호출
- [ ] `ActivateProvider`: repo 실패 → `cache.Invalidate` 미호출
- [ ] `UpdateProviderConfig`: 캐시된 Provider.ID == id → Invalidate 호출
- [ ] `UpdateProviderConfig`: 캐시된 Provider.ID != id → Invalidate 미호출
- [ ] `UpdateProviderConfig`: 캐시 비어있음 → Invalidate 미호출
- [ ] `DeleteProvider`: 동일 분기 검증
- [ ] `CreateProvider`: `Validate()` 실패 시 repo 미호출 (이름 빈 문자열 등)

### 7.2 핸들러 테스트 (httptest + mock service)
- [ ] `GET /api/v1/admin/llm-providers` → 200, JSON 배열
- [ ] `GET /:id` 잘못된 id ("abc") → 400
- [ ] `GET /:id` ErrProviderNotFound → 404
- [ ] `POST` 본문 누락 → 400
- [ ] `POST` provider_type 가 LOCAL/EXTERNAL 외 → 400 (Gin validator)
- [ ] `POST` 정상 → 201
- [ ] `PUT /:id/activate` ErrProviderNotFound → 404
- [ ] `PUT /:id/activate` 정상 → 200, "provider activated"
- [ ] `PATCH /:id/config` 정상 → 200, "config updated"
- [ ] `DELETE /:id` 정상 → 204
- [ ] 알 수 없는 에러 → 500, details 동봉
- [ ] `errors.Is` 체인 통과 검증: `fmt.Errorf("wrap: %w", domain.ErrProviderNotFound)` → 404

### 7.3 통합 테스트 (선택, `//go:build integration`)
- [ ] docker-compose 로 MariaDB 띄우고 마이그레이션 적용 후 전체 라이프사이클: Create → Activate → GetActive → UpdateConfig → Delete
- [ ] Activate 트랜잭션: DB 직접 쿼리로 is_active=1 인 행이 정확히 1개인지 확인
- [ ] Activate 후 `/api/v1/.../activate` 재호출(다른 ID)로 캐시 미반영 케이스 회귀

### 7.4 미들웨어 테스트
- [ ] Recovery: 패닉 핸들러를 호출하면 500 + zap 에 panic 로그
- [ ] Logger: 정상 요청 후 logger.Info 가 method/path/status/latency 필드 포함하여 1회 호출
- [ ] CORS: OPTIONS 요청 → 204 + Access-Control-* 헤더, 일반 요청에도 헤더 부착

### 7.5 검증 체크리스트
- [ ] `go build ./...` 통과 (단, `cmd/server` 만 빌드해도 충분; 기존 `internal/infrastructure/adapter/...` agentic 시스템은 별도 entry 가 없으면 사용처가 사라져도 컴파일은 유지됨)
- [ ] `go vet ./...` 통과
- [ ] race detector: `go test -race ./...`
- [ ] mock 패키지: `github.com/stretchr/testify/mock` 또는 손코딩 fake 권장 (gomock 도입은 선택)

---

## 8. 기존 코드와의 관계 / 주의사항

1. **기존 `cmd/server/main.go` 교체**: agentic loop 시스템(usecase/agentic_loop, infrastructure/adapter/inbound/rest, llm/ollama_adapter, mcp, session) 은 새 main 에서 더 이상 호출되지 않는다. 해당 패키지 파일은 보존되어 있어 추후 별도 entry(예: `cmd/agentic/main.go`) 또는 동일 main 에 통합 마운트가 가능하다. 본 단계에서는 LLM Provider Admin API 단일 책임에 집중했다.
2. **port 패키지 분리 유지**:
   - `internal/port/` — 본 작업의 LLM Provider 도메인 포트 (LLMRepository, LLMAdapter, LLMFactory, ProviderCache)
   - `internal/domain/port/` — 기존 agentic 도메인 포트 (LLMPort, MCPPort, SessionPort 등)
   둘은 의도적으로 분리되어 있으며 import 경로로 명확히 구분된다.
3. **adapter 패키지 분리 유지**:
   - `internal/adapter/{cache,db,llm}` — 본 작업
   - `internal/infrastructure/adapter/{inbound,outbound}/...` — 기존 agentic 시스템
4. **CORS 의존성**: `gin-contrib/cors` 미사용. 자작 미들웨어로 동등 동작 제공. 운영 적용 전 화이트리스트 기반으로 강화 필요.
5. **Singleflight 미적용**: 캐시 미스 동시 발생 시 multiple repo 호출 가능. 트래픽이 일정 수준 이상이면 `golang.org/x/sync/singleflight` 적용 권고.
6. **env 키 보안**: `domain.ProviderConfig.APIKeyEnv` 는 환경변수 **이름** 만 저장하는 계약. 핸들러/DTO 단계에서 시크릿 평문 저장을 막기 위한 추가 검증(예: 문자열이 "sk-" 로 시작하면 거부)을 향후 도입할 수 있다.
7. **go.mod 갱신**: `go-sql-driver/mysql` v1.8.1 + `filippo.io/edwards25519` v1.1.0 (mysql v1.8.1 의 indirect 의존성) 추가. 호스트에 Go 가 설치된 환경에서 `go mod tidy` 한 번 실행하면 go.sum 의 hash 가 자동 채워진다.

---

## 9. 후속 단계 (qa-engineer 인계)

1. 위 7장의 테스트 매트릭스를 우선순위대로 구현.
2. `httptest` + `gin.New(); r := router.New(...)` 패턴으로 e2e 핸들러 검증.
3. service 레이어 mock 은 다음 인터페이스 3종에 대해 작성 (port 패키지에 이미 정의):
   - `port.LLMRepository`
   - `port.ProviderCache`
   - `port.LLMFactory`
4. `domain.ErrXxx` 매핑 테이블이 핸들러와 일치하는지 회귀 테스트 추가.
