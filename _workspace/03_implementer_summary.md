# 03. 구현 요약 (implementer)

> 설계 `01_architect_design.md` + 데이터 요약 `02_data_summary.md` 기준으로
> HTTP 레이어 + application 유스케이스 + 보안(JWT/RBAC) + main.go(조립 루트) + 구 트리 정리 완료.
> Go 미설치 환경 → 빌드 미검증(시그니처·import·패키지명을 data_summary 와 한 글자도 어긋나지 않게 작성).

## 1. 생성 파일 목록 (신규)

| 경로 | 패키지 | 내용 |
|---|---|---|
| `com/ohmyagent/internal/application/auth/usecase.go` | `authapp` | `AuthUseCase`(`var _ domainauth.Service`). Login/멤버관리/게이트 |
| `com/ohmyagent/internal/application/llmprovider/usecase.go` | `llmproviderapp` | `ProviderService`(`var _ domainllmprovider.Service`). CRUD+활성화+캐시조율, accessGate 주입 |
| `com/ohmyagent/internal/adapter/out/llm/factory.go` | `llm` | `Factory`(`var _ domainllmprovider.Factory`) |
| `com/ohmyagent/internal/adapter/out/llm/cache.go` | `llm` | `Cache`(atomic.Value, `Get()(LLMProvider,bool)`/Set/Invalidate) |
| `com/ohmyagent/internal/adapter/out/llm/ollama_adapter.go` | `llm` | `OllamaAdapter`(LOCAL) |
| `com/ohmyagent/internal/adapter/out/llm/claude_adapter.go` | `llm` | `ClaudeAdapter`(EXTERNAL/claude) |
| `com/ohmyagent/internal/adapter/out/llm/openai_adapter.go` | `llm` | `OpenAIAdapter`(EXTERNAL/기타) |
| `com/ohmyagent/internal/adapter/in/http/handler.go` | `httpin` | `HandlerFunc`/`Handle()`/`AppError`/생성헬퍼/`writeJSON`/`atoiDefault` |
| `com/ohmyagent/internal/adapter/in/http/auth_handler.go` | `httpin` | `AuthHandler` + DTO + `toMemberResp` + `authErrToHTTP` |
| `com/ohmyagent/internal/adapter/in/http/llmprovider_handler.go` | `httpin` | `ProviderHandler` + DTO + `toProviderResp` + `providerErrToHTTP` |
| `com/ohmyagent/internal/adapter/in/http/security/claims.go` | `security` | `ctxKey`/`withClaims`/`ClaimsFrom` |
| `com/ohmyagent/internal/adapter/in/http/security/jwt.go` | `security` | `JWTTokenService`(`var _ domainauth.TokenService`, HS256, alg 고정) |
| `com/ohmyagent/internal/adapter/in/http/security/router.go` | `security` | `SecureRouter`/`Public`/`Secured`/`MinRole`/`Mux()` |
| `com/ohmyagent/internal/adapter/in/http/security/middleware.go` | `security` | `Chain`(logging→CORS), rs/cors(AllowedOrigins 비면 비활성) |
| `com/ohmyagent/cmd/api/main.go` | `main` | 조립 루트 + 라우트 등록 + 시딩 + graceful shutdown |

## 2. 등록된 라우트 (설계 §7 전부)

| 메서드·경로 | MinRole | 핸들러 |
|---|---|---|
| `POST /api/v1/auth/login` | Public | `authH.Login` |
| `GET /api/v1/members` | admin | `authH.ListMembers` |
| `POST /api/v1/members` | admin | `authH.CreateMember` |
| `GET /api/v1/members/{id}` | user | `authH.GetMember` |
| `PUT /api/v1/members/{id}/role` | admin | `authH.ChangeRole` |
| `PUT /api/v1/members/{id}/active` | admin | `authH.SetActive` |
| `DELETE /api/v1/members/{id}` | super_admin | `authH.DeleteMember` |
| `GET /api/v1/llm-providers` | user | `provH.List` |
| `GET /api/v1/llm-providers/{id}` | user | `provH.Get` |
| `POST /api/v1/llm-providers` | admin | `provH.Create` |
| `PATCH /api/v1/llm-providers/{id}/config` | admin | `provH.UpdateConfig` |
| `PUT /api/v1/llm-providers/{id}/activate` | admin | `provH.Activate` |
| `DELETE /api/v1/llm-providers/{id}` | admin | `provH.Delete` |

유스케이스 추가 게이트: 멤버 CRUD 는 `RequireAdmin`+`CanControl`, GetMember 는 본인/admin↑,
provider 변경계열은 accessGate(`RequireAdmin`) 강제. 조회계열은 라우트 게이트만.

## 3. main.go 조립 순서

1. `config.Load()` → `logger.Init`
2. `dbout.Open(driver,dsn,maxOpenConns)` → `defer conn.Close()` → `dbout.RunMigrations` (30s ctx)
3. repos: `NewRoleRepository`/`NewMemberRepository`/`NewLLMProviderRepository`
4. `authout.NewBcryptHasher(0)` / `security.NewJWTTokenService(secret, expiry.Std())` / `llmout.NewCache()` / `llmout.NewFactory()`
5. `authapp.NewAuthUseCase(memberRepo, roleRepo, hasher, tokenSvc)`
6. `llmproviderapp.NewProviderService(providerRepo, cache, factory, authUC)` — authUC 가 accessGate 충족
7. 비운영(`SeedsInitialAdmin()`)이면 `seedInitialAdmin`(super_admin 멤버 + 샘플 LOCAL provider; 중복/비밀번호 미설정 시 skip)
8. 핸들러 생성 → `SecureRouter` 라우트 등록 → `security.Chain(mux, allowedOrigins)` → `http.Server`
9. `signal.NotifyContext`(SIGINT/SIGTERM) + `srv.Shutdown`(15s) graceful shutdown

## 4. 삭제한 구 파일/디렉터리 (설계 §8 제거대상 1~10)

전체 삭제: `internal/adapter/db/`(+sqlc), `internal/application/usecase/`, `internal/config/`,
`internal/domain/`(llm_provider/errors/session/message/tool/port), `internal/dto/`,
`internal/infrastructure/`(전부), `internal/middleware/`, `internal/port/`, `internal/router/`,
`pkg/`, `cmd/`(server), `migrations/`(구), `config/`(단수), `db/`(sqlc 입력), `sqlc.yaml`.

비-test 파일만 삭제(디렉터리 보존): `internal/adapter/cache/provider_cache.go`,
`internal/adapter/llm/{claude,factory,ollama,openai}_adapter.go`,
`internal/application/llm_provider_service.go`, `internal/handler/llm_provider_handler.go`.

보존: `docker-compose.yml`(implementer 판단), `configs/`(복수형).

## 5. 남겨둔 test 파일 (⚠️ 사용자 승인 대기 — 절대 삭제·수정 안 함)

- `internal/adapter/cache/provider_cache_test.go`
- `internal/adapter/llm/factory_test.go`
- `internal/application/llm_provider_service_test.go`
- `internal/handler/llm_provider_handler_test.go`

이 4개는 모두 구 패키지 경로(`OhMyAgent.AiAgent.Server/internal/...`)·gin·int64 ID 에 묶여 있어
신규 구조에서 **컴파일 불가**다. 동반 비-test 파일이 삭제되어 패키지 컴파일이 깨지므로,
사용자 승인 후 (a) 재작성하거나 (b) 삭제해야 한다.

## 6. 빌드 시 예상 확인사항

- **`go.sum` 재생성 필요**: `go mod download` / `go mod tidy`(golang-jwt/jwt/v5, rs/cors, goose, uuid, sqlite, mysql, yaml, bcrypt). 현재 환경 Go 미설치 → 미검증.
- **goose Provider API**(v3.21): `migrate.go`(data-engineer 작성)가 사용. 최초 빌드 시 시그니처 확인.
- **out/db(package db)·out/llm(package llm) import alias 필수**: main.go 에서 `dbout`/`llmout` alias 사용함.
- **남은 4개 `*_test.go` 로 인해 `go build ./...`/`go test ./...` 는 구 패키지에서 실패**할 것. 신규 트리만 빌드하려면 `go build ./com/...` 로 한정하거나, 승인 후 test 정리 선행.
- JWT: `golang-jwt/jwt/v5` HS256, `jwt.WithValidMethods([]string{"HS256"})`로 alg 고정. 만료=`cfg.Auth.JWTExpiry`.
- 인가 에러 누출 매핑: provider 핸들러의 `providerErrToHTTP` 가 accessGate(authUC) 에서 새는 `domainauth.ErrPermission`/`ErrNotFound`를 403 으로 매핑함(도메인 경계 교차 처리).
