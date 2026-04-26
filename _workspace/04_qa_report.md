# QA 보고서 — LLM 제공자 동적 스위칭 시스템

본 보고서는 4단계 (qa-engineer) 산출물이다. HTTP 핸들러, Application 서비스,
캐시 어댑터, LLM 팩토리에 대한 단위 테스트를 작성하고, 경계면 정합성을 검증했다.

모듈명: `OhMyAgent.AiAgent.Server`
작성일: 2026-04-26

---

## 1. 작성된 테스트 파일 목록

| 파일 | 대상 | 종류 | 케이스 수 |
|------|------|------|----------|
| `internal/application/llm_provider_service_test.go` | `application.LLMProviderService` | 단위 (mock 3종) | 11 |
| `internal/handler/llm_provider_handler_test.go` | `handler.LLMProviderHandler` | httptest + table-driven | 6개 함수 / 23 sub-test |
| `internal/adapter/cache/provider_cache_test.go` | `cache.providerCache` | 단위 + 동시성 | 5 |
| `internal/adapter/llm/factory_test.go` | `llm.factory` | 단위 (table-driven) | 5개 함수 / 12 sub-test |

전체 약 50+ 케이스. 테스트 패키지 모두 `_test` suffix (블랙박스).

---

## 2. 경계면 검증 체크리스트

### 2.1 핸들러 ↔ DTO JSON 필드 정합성

| 엔드포인트 | 요청 필드 | 응답 필드 | 결과 |
|-----------|----------|-----------|------|
| POST   /llm-providers | `name`, `provider_type`, `is_active`, `config` | `id`, `name`, `is_active`, `provider_type`, `config`, `created_at`, `updated_at` | OK |
| PATCH  /llm-providers/:id/config | `config{endpoint,model,api_key_env,max_tokens,extra_params}` | `MessageResponse{message}` | OK |
| 활성화 응답 | — | `{"message":"provider activated"}` | OK (테스트로 회귀) |
| config 갱신 응답 | — | `{"message":"config updated"}` | OK |
| 에러 응답 | — | `ErrorResponse{error, details?}` | OK |

`ProviderConfigDTO` 의 5개 필드 (`endpoint`, `model`, `api_key_env`, `max_tokens`, `extra_params`) 모두
도메인 `ProviderConfig` 와 1:1 매핑. 핸들러의 `toProviderResponse` / `fromConfigDTO` 로 양방향 변환 확인.

### 2.2 HTTP 상태 코드 일관성

| 시나리오 | 핸들러 코드 | 검증 테스트 |
|----------|-----------|-----------|
| 정상 List/Get/Activate/UpdateConfig | 200 | TestListProviders/TestGetProvider/TestActivateProvider/TestUpdateProviderConfig |
| 정상 Create | 201 | TestCreateProvider |
| 정상 Delete | 204 | TestDeleteProvider |
| 잘못된 path id (`abc`, `-1`) | 400 | TestGetProvider/TestActivateProvider/TestUpdateProviderConfig/TestDeleteProvider |
| 빈 body / 필수 필드 누락 | 400 | TestCreateProvider, TestUpdateProviderConfig |
| `oneof=LOCAL EXTERNAL` 위반 | 400 | TestCreateProvider/invalid_provider_type |
| `ErrProviderNotFound` / `ErrNoActiveProvider` | 404 | TestGetProvider/TestActivateProvider/TestDeleteProvider/TestRespondError_WrappedSentinelDetection |
| `ErrProviderConflict` | 409 | TestCreateProvider/conflict |
| 알 수 없는 에러 | 500 | TestListProviders/internal_error |

### 2.3 서비스 → 핸들러 에러 타입 정합성 (`errors.Is`)

핸들러 `respondError` 의 sentinel 매칭이 `fmt.Errorf("...: %w", domain.ErrXxx)` 래핑된 에러도 인식하는지
회귀 테스트 추가:

- `TestRespondError_WrappedSentinelDetection` → 래핑된 `ErrNoActiveProvider` → 404
- `TestGetProvider/not_found_wrapped_error` → 래핑된 `ErrProviderNotFound` → 404
- `TestCreateProvider/conflict` → 래핑된 `ErrProviderConflict` → 409
- 서비스 단위 테스트의 `TestGetActiveLLMAdapter_NoActiveProvider`, `TestActivateProvider_NotFound`
  도 `errors.Is` 로 래핑 체인 검증.

### 2.4 응답 DTO ↔ 도메인 매핑

`TestGetProvider/success` 와 `TestCreateProvider/success` 에서:
- `ProviderResponse.ID == domain.LLMProvider.ID` (int64)
- `ProviderResponse.Name == domain.LLMProvider.Name`
- `ProviderResponse.ProviderType == string(domain.LLMProvider.ProviderType)`
- 시간 필드 직렬화 (RFC3339) 통과 확인.

---

## 3. 발견된 불일치 및 수정 내용

### 3.1 핸들러가 concrete service 에 의존 (수정 완료)

**위치:** `internal/handler/llm_provider_handler.go`

**원인:** `LLMProviderHandler` 가 `*application.LLMProviderService` (concrete pointer) 에 직접 의존 →
mock 주입 불가, 테스트 작성 시 무거운 dependency graph 필요.

**수정:**
- 핸들러 패키지 안에 인터페이스 `handler.LLMProviderServicePort` 정의
  (서비스 메서드 6개 동일 시그니처).
- 생성자 시그니처를 `NewLLMProviderHandler(svc LLMProviderServicePort)` 로 교체.
- `import "OhMyAgent.AiAgent.Server/internal/application"` 제거 → 순환 가능성 차단.
- concrete `*application.LLMProviderService` 가 메서드 시그니처를 모두 만족하므로 `cmd/server/main.go` 변경 불필요.

이 변경으로 `MockLLMProviderService` 를 핸들러 테스트에 직접 주입할 수 있게 됨.

### 3.2 testify 의존성이 go.mod 에 누락 (수정 완료)

**위치:** `go.mod`

**원인:** `go.sum` 에는 `stretchr/testify v1.8.4` 의 `.mod` 해시가 indirect 로 남아있으나
`go.mod` 의 `require` 블록에는 미선언 → 본 단계에서 직접 사용 시 컴파일 실패.

**수정:**
- `require` 블록에 다음 추가:
  - `github.com/stretchr/testify v1.8.4`
- indirect 블록에 다음 추가 (testify 의존성):
  - `github.com/davecgh/go-spew v1.1.1`
  - `github.com/pmezard/go-difflib v1.0.0`
  - `github.com/stretchr/objx v0.5.0`
- `gopkg.in/yaml.v3 v3.0.1` 는 이미 존재.

**주의:** 본 환경에는 Go 가 설치되어 있지 않다 (`which go` → not found). 빌드 환경에서
`go mod tidy && go mod download` 를 실행해 `go.sum` 의 zip 해시를 보충해야 한다 (현재 go.sum 에는 .mod 해시만 존재).

### 3.3 발견되었으나 수정하지 않은 항목

- 캐시 어댑터(`internal/adapter/cache/provider_cache.go`)의 `Set` 은 `nil` 인자 방어 코드 없음. 단,
  도메인 컨벤션 상 호출 측에서 nil 을 넘기지 않도록 통제하므로 (`s.cache.Set(provider)` 는 항상 fetched provider) 부작용 없음.
- 핸들러는 `ListProviders` 에서 빈 배열을 `[]dto.ProviderResponse{}` 로 반환 (`make([]dto.ProviderResponse, 0, ...)`) →
  JSON 직렬화 시 `null` 이 아닌 `[]` 로 나오는지 회귀 테스트 추가됨 (`TestListProviders/empty`).
- `ActivateProviderRequest` 가 빈 구조체로 정의되어 있으나 핸들러에서 미사용. 미래 확장 여지로 보존.

---

## 4. 테스트 실행 명령어

```bash
# 의존성 보강 (최초 1회)
go mod tidy
go mod download

# 전체 테스트 실행
go test ./...

# race detector 활성화 (캐시 동시성 검증 필수)
go test -race ./...

# 패키지별
go test -v ./internal/application/...
go test -v ./internal/handler/...
go test -v -race ./internal/adapter/cache/...
go test -v ./internal/adapter/llm/...

# 커버리지
go test -cover ./internal/...
```

---

## 5. 알려진 한계사항

1. **Go 미설치 환경:** 본 환경에서는 `go test` 실행 불가. 모든 테스트 코드는 정적 분석만 수행. 사용자 환경에서 `go mod tidy` 실행 후 `go test ./...` 로 검증 필요.
2. **DB 어댑터 테스트 미포함:** `internal/adapter/db/...` 의 sqlc 기반 레포지토리는 통합 테스트(`//go:build integration`) 영역으로 분리. 본 단계에서는 `port.LLMRepository` 를 mock 으로 대체하여 application/handler 만 검증.
3. **미들웨어 테스트 미포함:** `internal/middleware/{recovery,logger,cors}.go` 는 향후 별도 세트로 작성 권장.
4. **LLM 어댑터 stub 검증 미포함:** ollama/claude/openai adapter 의 `Complete()` 가 모두 스텁 문자열을 반환하므로 별도 테스트 가치 낮음. 실제 HTTP 호출 구현 후 어댑터 테스트 추가.
5. **Singleflight 미적용:** 캐시 미스 동시 발생 시 multiple repo 호출 가능. 동시성 테스트는 캐시 자체의 race-free 만 보장.
6. **Auth/Authn 미검증:** 현재 핸들러에 인증 미들웨어가 없으므로 권한 테스트 불필요. 향후 인증 도입 시 401/403 케이스 추가.
7. **시간 필드 비교:** `TestGetProvider/success` 는 `time.Time` 필드 직렬화 통과만 확인하고 정확한 RFC3339 매칭은 생략. 필요 시 `assert.WithinDuration` 추가.

---

## 6. 회귀 보장 요약

- DTO JSON 태그 변경 시 → handler 테스트의 unmarshal 단계에서 fail.
- `domain.ErrXxx` 추가/변경 시 → handler 테스트의 status 매핑 검증에서 fail.
- 서비스 메서드 시그니처 변경 시 → `LLMProviderServicePort` 와 `*LLMProviderService` 간 인터페이스 만족 실패로 컴파일 fail (main.go 단계).
- 캐시 atomic.Value 의 typed-nil 처리 회귀 → `TestProviderCache_Invalidate` 가 panic 으로 fail.
- 팩토리 분기 규칙 변경 (모델명 키워드) 시 → `TestFactory_CreateExternalClaude/openai` 의 `IsType` assertion 으로 즉시 fail.

---

## 7. 후속 단계 권고

1. **CI 통합:** GitHub Actions / GitLab CI 에 `go test -race -coverprofile=cov.out ./...` 단계 추가.
2. **통합 테스트:** docker-compose 로 MariaDB 띄우고 마이그레이션 + 라이프사이클 e2e (`//go:build integration`).
3. **fuzz 테스트:** `parseIDParam`, JSON binding 에 대해 `testing.F` 적용.
4. **벤치마크:** `BenchmarkProviderCache_Get/Set` 으로 atomic.Value vs sync.RWMutex 정량 비교.
