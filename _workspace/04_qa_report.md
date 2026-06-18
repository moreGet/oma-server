# 04. QA 보고서 (qa-engineer)

> 대상: `TEMPLATE-SPEC.md` 기준 재구성된 헥사고날 신규 코드(`com/ohmyagent/` 이하).
> 구 테스트(구 `internal/...` 트리)는 이미 삭제되어 존재하지 않음 → 신규 테스트를 처음부터 작성.
> Go 미설치 환경 → 실행 미검증. 시그니처·import·필드명·에러변수명을 실제 소스와 대조해 작성함.

## 1. 작성한 테스트 파일

| 경로 | package | 대상/커버 |
|---|---|---|
| `com/ohmyagent/internal/domain/auth/model_test.go` | `auth` (내부) | `RoleLevel.CanControl`(상위/동급/하위 7케이스), `CreateMemberCommand.Validate`(정상·빈/공백 username·짧은 pw·role_id 범위), `Normalize` 트림 |
| `com/ohmyagent/internal/domain/llmprovider/model_test.go` | `llmprovider` (내부) | `CreateCommand.Validate`(LOCAL/EXTERNAL 정상·빈/공백 name·잘못된/빈 provider_type), `Normalize` |
| `com/ohmyagent/internal/application/auth/usecase_test.go` | `authapp` (내부) | fake Repository/RoleRepository/TokenService/PasswordHasher 주입. Login(불일치·미존재·비활성→ErrInvalidCredentials, 성공→토큰+멤버), CreateMember(검증·비admin·CanControl·중복 ErrConflict·성공 시 uuid/감사/시각), ChangeRole·SetActive·Delete의 CanControl 인가, GetMember(본인/admin↑/거부), RequireAdmin(admin pass·user·비활성·미존재) |
| `com/ohmyagent/internal/application/llmprovider/usecase_test.go` | `llmproviderapp` (내부) | fake Repository/Cache/Factory/Adapter + 비공개 `accessGate` fake 주입. Create/UpdateConfig/Activate/Delete의 gate 거부 차단, 성공 시 uuid/시각/감사, 활성 생성·UpdateConfig·Activate·Delete 후 `cache.Invalidate` 호출 검증, repo 에러 시 무효화 안 함, GetActiveAdapter 캐시 hit(repo 미호출)/miss(repo→cache.Set→factory)/무활성 에러 |
| `com/ohmyagent/internal/adapter/out/llm/cache_test.go` | `llm` (내부) | Get 초기 false, Set→Get true+값, Invalidate→Get false, 동시성(-race) 1건 |
| `com/ohmyagent/internal/adapter/in/http/auth_handler_test.go` | `httpin` (내부) | fake `domainauth.Service` 주입 + 실제 `SecureRouter`로 claims 주입. Login 200(토큰/멤버, **password_hash 미노출** 검증)·401·400, GetMember 200(actorID 전파)·404·403, CreateMember 201·400·409 |
| `com/ohmyagent/internal/adapter/in/http/llmprovider_handler_test.go` | `httpin` (내부) | fake `domainllmprovider.Service`. Create 201·400·**누출된 `domainauth.ErrPermission`→403**, List 200, Activate 200(message)·404, Delete 204·404 |
| `com/ohmyagent/internal/adapter/in/http/security/router_test.go` | `security` (내부) | MinRole 게이트: 토큰없음 401·잘못된토큰 401·레벨부족 403·충분 200·상위 200, claims context 주입 검증 |

총 8개 테스트 파일. fake는 각 테스트 파일 내부에 정의(별도 mock 라이브러리 미사용, `testify/assert`·`require`만 사용).

### 설계상의 핵심 정합성 확인 사항
- 핸들러 테스트는 `security.withClaims`가 비공개이므로 claims를 직접 주입할 수 없어, 실제 `SecureRouter.Secured` + 실제 `JWTTokenService`(HS256)로 토큰을 발급해 라우팅했다. 이로써 라우트 게이트 ↔ 핸들러 ↔ 에러매핑 경계면을 함께 검증한다.
- 응답 DTO에 `PasswordHash`/`password_hash`가 노출되지 않음을 본문 문자열·JSON 키 두 방법으로 확인.
- AppError 매핑: ErrValidation→400, ErrInvalidCredentials/ErrInvalidToken→401, ErrPermission→403, ErrNotFound→404, ErrConflict→409. provider 핸들러의 accessGate 누출(`domainauth.ErrPermission`)→403 매핑도 별도 케이스로 검증.

## 2. 발견한 경계면 이슈

### (해소됨) 03 요약 §5의 "남은 4개 *_test.go" — 실제로는 존재하지 않음
- 03_implementer_summary.md §5는 구 `internal/adapter/cache|llm`, `internal/application`, `internal/handler`의 `*_test.go` 4개가 컴파일을 깨뜨리니 승인 후 정리하라고 기록.
- **실측 결과**: `internal/` 트리 전체가 이미 삭제되어 존재하지 않고, 리포 전체에 `*_test.go`가 0개였음(`find . -name '*_test.go'` 무결과). 따라서 구 테스트로 인한 컴파일 위험은 **현재 없음**. 본 작업의 신규 테스트가 첫 테스트다.

### 이슈 #1 (정보) — 모듈 루트는 리포 루트
- `go.mod`(module `aiagent`)는 **리포 루트**(`OhMyAgent.AiAgent.Server/go.mod`)에 있고 코드는 `com/ohmyagent/...` 하위에 위치. import 경로 `aiagent/com/ohmyagent/internal/...`는 이 구조와 일치(정상). `go build`/`go test`는 리포 루트에서 실행해야 한다(`com/ohmyagent/go.mod`는 없음).

### 이슈 #2 (경미) — `SetActive`의 `_ = actor` 죽은 코드
- `application/auth/usecase.go` `SetActive`(L194)에서 `requireControl`이 반환한 `actor`를 `_ = actor`로 폐기. 기능상 무해하나 불필요. 테스트는 동작(권한·갱신)만 검증하며 이 라인에 의존하지 않음. 제안: 정리 시 `_, target, err := ...`로 변경.

### 이슈 #3 (정보) — provider 조회계열(List/Get)의 actorID 미사용
- `ProviderService.List/Get`은 `actorID`를 받지만 사용하지 않음(라우트 MinRole 게이트로만 보호, 설계 명시). 의도된 설계이므로 이슈 아님 — 기록만.

### 이슈 #4 (확인필요·빌드) — 빌드 검증 불가(Go 미설치)
- 본 환경에 Go 미설치로 `go build`/`go vet`/`go test` 미실행. 테스트 코드의 import·시그니처·필드명은 소스와 대조했으나, **`go.sum` 재생성 및 의존성(testify, golang-jwt/v5, uuid 등) 다운로드 후 최초 컴파일은 사용자 환경에서 필요**.

> 컴파일을 실제로 깨뜨릴 수준의 설계↔구현 불일치는 발견되지 않음(이슈 #1~#4 모두 경미/정보/환경). 따라서 테스트로 우회·차단한 항목 없음.

## 3. 빌드/실행 권장 명령 (리포 루트에서)

```bash
cd OhMyAgent.AiAgent.Server
go mod tidy            # go.sum 재생성 (testify/golang-jwt/uuid/goose/cors/sqlite/mysql/yaml/bcrypt)
go build ./...         # 신규 트리 전체 컴파일
go vet ./...
go test ./...          # 전체 테스트
go test -race ./com/ohmyagent/internal/adapter/out/llm/...   # 캐시 동시성 검증
go test ./... -cover   # 커버리지
```

## 4. 커버리지 요약(의도)
- **도메인 순수 로직**: RoleLevel 비교·두 커맨드 Validate/Normalize 전 분기 커버.
- **application(유스케이스)**: 인증·인가 게이트(CanControl, RequireAdmin), 캐시 무효화 트리거, 캐시 hit/miss 폴백 흐름을 fake로 결정적 커버.
- **adapter/out/llm 캐시**: 상태 전이(empty→set→invalidate) + race.
- **adapter/in/http**: 정상 상태코드/JSON + 전 도메인 에러→HTTP 매핑 + 비밀필드 미노출.
- **security**: RBAC 라우트 게이트(401/403/통과) + claims 주입.
- 미커버(의도): DB repository(실DB·통합 테스트 영역), JWT 만료/alg-confusion 음성 케이스, main.go 조립/시딩.
