# Go 헥사고날 백엔드 템플릿 명세 (Template Spec)

> 이 문서는 `review-bot-api`의 구조를 **재사용 가능한 프로젝트 템플릿**으로 추출한 규칙서다.
> 새 프로젝트를 시작하거나 Claude Code로 새 도메인·기능을 생성할 때, 이 문서를 **규칙(rule)** 으로 삼아 일관된 구조·권한·컨벤션을 적용한다.
>
> **사용법:** 새 기능/도메인 작업 시 → 이 문서의 [§9 새 도메인 추가 체크리스트](#9-새-도메인-추가-체크리스트)를 그대로 따른다.
> 충돌 시 우선순위: `CLAUDE.md` > 이 문서 > 개별 스킬(go-api-architect, go-api-data, go-api-implementer, go-api-test).

---

## 0. 한눈에 보는 템플릿 정체성

| 항목 | 값 |
|------|-----|
| 언어/런타임 | Go 1.25 |
| 아키텍처 | 헥사고날(포트 & 어댑터), 의존성 역전 |
| 표준 라이브러리 우선 | `net/http`(Go 1.22+ 라우팅), `database/sql`, `log/slog` |
| 인증 | JWT (Bearer) + 라우트 레벨 RBAC |
| 권한 모델 | 3단계 역할 계층(user/admin/super_admin) + 리소스 소유권 게이트 |
| DB | `database/sql` + goose 마이그레이션 + (선택적) sqlc |
| 시각 저장 | `BIGINT`(unix epoch seconds) |
| ID 전략 | 도메인 엔티티 UUID v4(`VARCHAR(36)`), 마스터 데이터 `INT` |
| 설정 | 환경별 YAML(`configs/{APP_ENV}.yaml`) + 검증 |
| 외부 비밀 | AES-GCM 암호화 후 DB 저장, 평문/토큰 로그 금지 |
| 에러 모델 | 도메인 sentinel/typed 에러 → HTTP `AppError` 매핑 |

---

## 1. 절대 원칙 (위반 = 리뷰 거부)

1. **의존성은 바깥→안쪽으로만 흐른다.** `adapter → application → domain`. 도메인은 어댑터·외부 라이브러리를 import하지 않는다(`time`, 표준 stdlib 일부 예외).
2. **포트는 소비자(domain/application)가 정의한다.** 어댑터는 포트를 *구현*만 한다. 이름은 능력 중심(`Repository`, `Sanitizer`, `SecretCipher`), 구현 기술 중심 금지(`MySQLClient` ❌).
3. **도메인 타입만 레이어 경계를 넘는다.** GitLab/OpenAI/HTTP/SQL 타입이 도메인·유스케이스에 노출되면 위반.
4. **인증(authentication)은 라우터에서, 인가(authorization)는 유스케이스에서.** 라우트의 `MinRole`은 최소 게이트일 뿐, 소유권·필드별 권한은 유스케이스가 강제한다.
5. **에러를 삼키지 않는다.** `_ = err` 금지. 래핑은 `fmt.Errorf("context: %w", err)`, 비교는 `errors.Is/As`.
6. **비밀은 코드에 하드코딩하지 않는다.** 설정(env/yaml)으로만, DB 저장 시 암호화, 로그에 PII·토큰 금지.
7. **`*_test.go`는 사용자 승인 후에만 변경한다** (CLAUDE.md 테스트 보호 룰).

---

## 2. 디렉터리 레이아웃 (그대로 복제)

```
{repo}/
├── com/{org}/                          # 소스 루트 (예: com/ktis)
│   ├── cmd/api/main.go                 # 조립 루트(DI) + 라우팅 + graceful shutdown
│   └── internal/
│       ├── config/                     # config.go (YAML 로드·검증)
│       ├── logger/                     # slog 초기화
│       ├── domain/{aggregate}/         # 핵심: model.go / port.go / service.go
│       ├── application/{aggregate}/    # 유스케이스(흐름 조율)
│       └── adapter/
│           ├── in/http/                # 핸들러 + DTO + 에러매핑 + security/
│           │   └── security/           # SecureRouter, RBAC, CORS/CSRF, JWT
│           └── out/{service}/          # 포트 구현(db, gitlab, openai, slack, …)
│               └── db/
│                   ├── migrations/     # NNNNN_*.sql (goose, //go:embed)
│                   └── sqlcdb/         # (선택) sqlc 생성 코드
├── configs/{env}.yaml                  # local/dev/docker/prod
├── db/{schema,queries}/                # (선택) sqlc 입력
└── docs/                               # API-SPEC.md, 이 문서 등
```

**모듈/패키지 명명**
- 모듈 경로: `{module}` (예: `reviewbot`). go.mod와 모든 import의 루트.
- 도메인 패키지 import alias: `domain{aggregate}` (예: `domainauth`)
- 애플리케이션: `{aggregate}app` (예: `authapp`, `reviewapp`)
- 인바운드 HTTP: `httpin`
- 한 어그리거트 = `domain/X` + `application/X` 1:1 쌍.

---

## 3. 레이어별 책임 & 코드 스켈레톤

### 3.1 `domain/{x}/model.go` — 순수 데이터 + 검증 + 도메인 에러

규칙: 외부 import 금지(`time` 등 stdlib 제외). 엔티티·커맨드·sentinel 에러·검증 로직·순수 판정 함수를 둔다.

```go
package x

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러: 핸들러가 HTTP 코드로 매핑하는 계약 ---
var ErrNotFound = errors.New("x not found")              // → 404

type ErrValidation struct{ Msg string }                  // → 400
func (e *ErrValidation) Error() string { return e.Msg }

var ErrPermission = errors.New("permission denied")      // → 403

// --- 엔티티 ---
type X struct {
	ID        string    // UUID v4
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy string    // actor member ID, 시스템 생성이면 ""
	UpdatedBy string
}

// --- 커맨드(입력 의도) + 정규화/검증을 모델이 소유 ---
type CreateCommand struct {
	Name    string
	ActorID string
}

func (c *CreateCommand) Normalize() { c.Name = strings.TrimSpace(c.Name) }

func (c *CreateCommand) Validate() error {
	c.Normalize()
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	return nil
}
```

### 3.2 `domain/{x}/port.go` — 포트 인터페이스

규칙: in 포트(`Service`, 유스케이스가 노출하는 능력)와 out 포트(`Repository`, `Sanitizer` 등 외부 의존)를 함께 정의. 메서드 첫 인자는 항상 `ctx context.Context`.

```go
package x

import "context"

// Service는 유스케이스 포트(in)다.
type Service interface {
	Create(ctx context.Context, cmd CreateCommand) (X, error)
	Get(ctx context.Context, id string) (X, error)
	List(ctx context.Context, limit, offset int) ([]X, int, error)
}

// Repository는 영속화 포트(out)다.
type Repository interface {
	Save(ctx context.Context, x X) error
	FindByID(ctx context.Context, id string) (X, error)        // 없으면 ErrNotFound
	List(ctx context.Context, limit, offset int) ([]X, int, error)
}
```

### 3.3 `domain/{x}/service.go` — 순수 도메인 로직(부수효과 無)

선택적. 외부 호출 없는 계산/판정만(예: `IsActivePopup(now)`, `ClampListLimit`). 테스트하기 쉬운 함수로 둔다.

### 3.4 `application/{x}/usecase.go` — 흐름 조율 + 인가 강제

규칙: 포트만 조합한다. 외부 API 직접 호출 금지. ID/시각/감사필드 채움, 인가 게이트, 트랜잭션 경계를 여기서 처리. 컴파일 타임 구현 확인 필수.

```go
package xapp

import (
	"context"
	"time"

	"github.com/google/uuid"
	domainx "{module}/com/{org}/internal/domain/x"
)

var _ domainx.Service = (*XService)(nil) // 컴파일 타임 인터페이스 확인

type XService struct {
	repo domainx.Repository
	gate accessGate              // 인가용. authUC를 주입(§4.4)
}

// accessGate: auth 도메인을 직접 import하지 않기 위한 소비자 측 최소 인터페이스.
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}

func NewXService(repo domainx.Repository, gate accessGate) *XService {
	return &XService{repo: repo, gate: gate}
}

func (s *XService) Create(ctx context.Context, cmd domainx.CreateCommand) (domainx.X, error) {
	if err := cmd.Validate(); err != nil {
		return domainx.X{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	x := domainx.X{
		ID: uuid.NewString(), Name: cmd.Name,
		CreatedAt: now, UpdatedAt: now,
		CreatedBy: cmd.ActorID, UpdatedBy: cmd.ActorID,
	}
	if err := s.repo.Save(ctx, x); err != nil {
		return domainx.X{}, err
	}
	return x, nil
}
```

### 3.5 `adapter/in/http/{x}_handler.go` — HTTP ↔ 도메인 변환

규칙: 핸들러 시그니처는 `func(w, r) error`(§5.1). claims에서 actorID 추출, JSON 디코딩→커맨드 변환, 응답 DTO 직렬화, 도메인 에러→`AppError` 매핑. 외부 API 타입 import 금지. DTO와 매핑 함수는 같은 파일 하단에.

```go
package httpin

func (h *XHandler) Create(w http.ResponseWriter, r *http.Request) error {
	defer r.Body.Close()
	claims, _ := security.ClaimsFrom(r.Context())

	var req xWriteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	x, err := h.svc.Create(r.Context(), domainx.CreateCommand{
		Name: req.Name, ActorID: claims.MemberID,
	})
	if err != nil {
		return xErrToHTTP(err) // 도메인 에러 → AppError
	}
	writeJSON(w, http.StatusCreated, toXResp(x))
	return nil
}
```

### 3.6 `adapter/out/db/{x}_repository.go` — 포트 구현(영속화)

규칙: 포트를 구현하고 SQL ↔ 도메인 변환을 책임진다. 컴파일 타임 확인 + `scanX(rowScanner)` 헬퍼 + `time.Unix` 변환 + 0행→`ErrNotFound`. 패턴은 §6 참조.

### 3.7 `cmd/api/main.go` — 조립 루트(DI)

규칙: 모든 와이어링을 여기서 한다. 어댑터 생성 → 유스케이스 주입 → 핸들러 생성 → 라우트 등록 → 미들웨어 체인 → graceful shutdown. **비즈니스 로직 금지**, 순수 조립만.

---

## 4. 권한·인증 모델 (RBAC)

### 4.1 역할 계층

| 역할 | `role_id` | `role_level`(코드 상수) | 권한 범위 |
|------|-----------|------------------------|-----------|
| `user` | 1 | `RoleLevelUser = 0` | 본인 정보, 본인 소유 리소스 CRUD |
| `admin` | 2 | `RoleLevelAdmin = 1` | 멤버/설정 관리, 전 프로젝트 접근 |
| `super_admin` | 3 | `RoleLevelSuperAdmin = 2` | 전체 제어 + 삭제 + 전역 설정 |

```go
// domain/auth/model.go
type RoleLevel int
const (
	RoleLevelUser       RoleLevel = 0
	RoleLevelAdmin      RoleLevel = 1
	RoleLevelSuperAdmin RoleLevel = 2
)
// 상위만 하위를 제어: actor가 target보다 높아야 한다.
func (actor RoleLevel) CanControl(target RoleLevel) bool { return actor > target }
```

> ⚠️ `role_id`(1/2/3, DB·API 노출값)와 `role_level`(0/1/2, 권한 비교용 코드 상수)을 혼동하지 말 것.

### 4.2 라우트 레벨 인가 — `SecureRouter` (Spring Security `@PreAuthorize` 등가)

`main.go`에서 라우트마다 최소 역할을 선언한다. JWT 파싱 실패 → 401, 역할 부족 → 403.

```go
router := security.NewSecureRouter(mux, tokenSvc)

// 1) 공개(인증 불필요)
router.Public("POST /api/v1/auth/login", httpin.Handle(authH.Login))

// 2) 인증된 사용자(any user)
router.Secured("GET /api/v1/x/{id}", httpin.Handle(xH.Get), security.MinRole(domainauth.RoleLevelUser))

// 3) admin 전용
router.Secured("POST /api/v1/x", httpin.Handle(xH.Create), security.MinRole(domainauth.RoleLevelAdmin))

// 4) super_admin 전용(주로 삭제)
router.Secured("DELETE /api/v1/members/{id}", httpin.Handle(memberH.Delete), security.MinRole(domainauth.RoleLevelSuperAdmin))
```

핵심 동작(security/router.go):
- `Secured`는 `Authorization: Bearer` 파싱 → `claims.Level < minLevel`이면 403.
- 통과 시 `claims`를 `context`에 주입. 핸들러는 `security.ClaimsFrom(ctx)`로 actor를 얻는다.

### 4.3 인가 패턴 선택 규칙 (어디서 막을 것인가)

| 상황 | 강제 위치 | 방법 |
|------|----------|------|
| "이 역할 이상만 접근" (단순 게이트) | 라우터 | `MinRole(...)` |
| "본인 소유 리소스만" | 유스케이스 | 소유권 검사(`CreatedBy == actorID`) |
| "필드별 권한 차등" (예: 특정 필드는 super_admin만) | 유스케이스 | 필드 nil 체크 + 레벨 비교 |
| "프로젝트/리소스 접근권" | 유스케이스 | `EnsureProjectAccess` 게이트 |

> 원칙: **라우트의 `MinRole`은 가장 느슨한 하한선**으로 두고, 세밀한 인가는 유스케이스에서 강제한다.
> 예) 게시판 쓰기는 라우트에선 `MinRole(User)`만 걸고, "본인 또는 admin"은 유스케이스 게이트로 처리.

### 4.4 인가 게이트 헬퍼 (auth 유스케이스가 제공)

```go
// application/auth/usecase.go — 모든 게이트의 기초
func (u *AuthUseCase) requireActiveMember(ctx context.Context, actorID string) (domainauth.Member, error) {
	actor, err := u.members.FindByID(ctx, actorID)
	if err != nil { return domainauth.Member{}, err }
	if !actor.Active { return domainauth.Member{}, domainauth.ErrPermission }
	return actor, nil
}

// admin↑ 게이트(타 도메인이 재사용하도록 public 노출)
func (u *AuthUseCase) RequireAdmin(ctx context.Context, actorID string) error {
	actor, err := u.requireActiveMember(ctx, actorID)
	if err != nil { return err }
	if actor.Role.Level < domainauth.RoleLevelAdmin { return domainauth.ErrPermission }
	return nil
}

// 프로젝트 접근: admin↑ 통과, 아니면 리소스 소유자만
func (u *AuthUseCase) EnsureProjectAccess(ctx context.Context, actorID string, projectID int) error {
	actor, err := u.requireActiveMember(ctx, actorID)
	if err != nil { return err }
	if actor.Role.Level >= domainauth.RoleLevelAdmin { return nil }
	token, err := u.projectTokens.FindByProjectID(ctx, projectID)
	if err != nil { return err }
	if token.CreatedBy == nil || *token.CreatedBy != actorID { return domainauth.ErrPermission }
	return nil
}
```

**타 도메인에서 재사용(의존성 역전):** auth를 직접 import하지 않고 최소 인터페이스만 의존하고, `main.go`에서 `authUC`를 주입한다.

```go
// application/x/usecase.go
type accessGate interface {
	RequireAdmin(ctx context.Context, actorID string) error
}
// main.go: boardSvc := boardapp.NewBoardService(repo, sanitizer, authUC)  // authUC가 accessGate 충족
```

### 4.5 필드별/소유권 차등 인가 예시

```go
func (u *AuthUseCase) UpdateProjectReviewSettings(ctx context.Context, actorID string, projectID int, cmd ...) (..., error) {
	actor, err := u.requireActiveMember(ctx, actorID)
	if err != nil { return ..., err }
	// (1) 특정 필드는 super_admin만
	if cmd.ReviewScope != nil && actor.Role.Level < domainauth.RoleLevelSuperAdmin {
		return ..., domainauth.ErrPermission
	}
	// (2) 나머지 필드는 소유자 또는 admin↑
	token, err := u.projectTokens.FindByProjectID(ctx, projectID)
	if err != nil { return ..., err }
	if actor.Role.Level < domainauth.RoleLevelAdmin {
		if token.CreatedBy == nil || *token.CreatedBy != actorID {
			return ..., domainauth.ErrPermission
		}
	}
	// ...
}
```

### 4.6 JWT 클레임

```go
type Claims struct {
	MemberID string
	Username string
	Level    RoleLevel
}
```
- 토큰 발급: 로그인 성공 시 `TokenService.Generate(member)`.
- 만료: `auth.jwt_expiry`(config). 비밀: `auth.jwt_secret`(config, 하드코딩 금지).

### 4.7 머신 투 머신 인증(웹훅/외부 호출)

JWT가 아닌 경로(예: GitLab 웹훅)는 별도 토큰을 검증한다. `X-Gitlab-Token` 또는 `Authorization: Bearer`에서 프로젝트 토큰을 추출 → DB 해시 대조 → 프로젝트 일치 검증. 토큰은 평문 저장 금지(SHA-256 해시 + last-four만 표시용 보관).

---

## 5. HTTP 레이어 컨벤션

### 5.1 핸들러 어댑터 — `error`를 반환하는 핸들러 + 중앙 처리

모든 핸들러는 `func(w, r) error`로 작성하고 `Handle()`로 감싼다. 패닉 복구·에러→JSON 변환·5xx 로깅을 한 곳에서 처리한다.

```go
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func Handle(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() { /* recover → 500 + 스택 로깅 */ }()
		if err := h(w, r); err != nil {
			ae := toAppError(err)
			if ae.HTTPStatus() >= 500 { slog.Error("request handler failed", ...) }
			writeJSON(w, ae.HTTPStatus(), ae)
		}
	}
}
```

### 5.2 표준 에러 모델 — `AppError`

에러 응답 envelope은 전 API 공통이다.

```json
{ "code": "BAD_REQUEST", "message": "name is required" }
```

| `code` | HTTP | 의미 |
|--------|------|------|
| `BAD_REQUEST` | 400 | 입력/형식 오류 |
| `UNAUTHORIZED` | 401 | 인증 실패 |
| `FORBIDDEN` | 403 | 권한 부족 |
| `NOT_FOUND` | 404 | 리소스 없음 |
| `METHOD_NOT_ALLOWED` | 405 | 메서드 불가 |
| `BAD_GATEWAY` | 502 | 외부 서비스(GitLab·AI 등) 오류 |
| `INTERNAL_ERROR` | 500 | 서버 내부 오류 |

생성 헬퍼: `ErrBadRequest(msg)`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`, `ErrBadGateway`, `ErrInternal`.

### 5.3 도메인 에러 → HTTP 매핑

각 도메인은 자기 에러→`AppError` 매핑 함수를 핸들러 파일에 둔다. **도메인 에러가 다른 도메인의 매핑으로 새지 않게** 도메인별로 분리한다.

```go
func xErrToHTTP(err error) error {
	var ve *domainx.ErrValidation
	switch {
	case errors.As(err, &ve):              return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainx.ErrNotFound):   return ErrNotFound("x not found")
	case errors.Is(err, domainx.ErrPermission): return ErrForbidden("permission denied")
	default:                               return err // → Handle()에서 500
	}
}
```

### 5.4 요청/응답 DTO 규칙

- 요청 DTO: `xWriteReq`, 응답 DTO: `xResp`, 목록: `xListResp{ total, limit, offset, items }`.
- JSON 태그는 `snake_case`. nullable은 포인터 + `omitempty`.
- 매핑 함수 `toXResp(domainx.X) xResp`로 도메인→응답 변환을 격리.
- 페이지네이션 기본: `limit=20`(상한 100), `offset=0`. 쿼리파라미터 파싱은 `atoiDefault` 헬퍼.
- 시각은 응답에서 `time.Time`(RFC3339 직렬화), nullable 시각은 `*time.Time`.

### 5.5 응답 직렬화

```go
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```
- 생성=`201`, 조회/수정=`200`, 삭제=`204 No Content`.

### 5.6 미들웨어 체인 (바깥→안)

`main.go`에서 `mux`를 다음 순서로 감싼다:
```
loggingMiddleware( CORS( CSRF( mux ) ) )
```
- **CORS/CSRF**: `AllowedOrigins`/`TrustedOrigins` 비면 비활성(로컬 dev). loopback(127.0.0.1, ::1)은 우회. 웹훅 경로는 CSRF 면제.
- **logging**: method/path/status/duration/remote 구조화 로깅(`slog`).

---

## 6. DB 컨벤션

### 6.1 스키마 규칙

```sql
CREATE TABLE IF NOT EXISTS members (
    id          VARCHAR(36)  NOT NULL PRIMARY KEY,  -- UUID v4
    username    VARCHAR(100) NOT NULL UNIQUE,
    active      BOOLEAN      NOT NULL DEFAULT TRUE,
    role_id     INT          NOT NULL,
    created_at  BIGINT       NOT NULL,              -- unix epoch seconds
    updated_at  BIGINT       NOT NULL,
    created_by  VARCHAR(36),                        -- actor member id, NULL=시스템
    updated_by  VARCHAR(36),
    FOREIGN KEY (role_id) REFERENCES roles(id)
);
```

| 항목 | 규칙 |
|------|------|
| PK | 엔티티=`VARCHAR(36)` UUID, 마스터 데이터(roles 등)=`INT` |
| 시각 | `BIGINT` unix seconds. Go: 저장 `t.Unix()`, 복원 `time.Unix(n, 0).UTC()` |
| 감사 컬럼 | `created_at, updated_at`(필수) + `created_by, updated_by`(nullable, NULL=시스템) |
| Soft delete | `deleted_at BIGINT`(NULL=활성), `deleted_by VARCHAR(36)`. 활성 유니크는 부분 인덱스 |
| 인덱스 명명 | `idx_{table}_{columns}` |
| 무기한/없음 표현 | `0`을 sentinel로(예: `end_at=0`=무기한), Go에서 zero-time ↔ 0 변환 |

### 6.2 마이그레이션 — goose + embed

```go
//go:embed migrations/*.sql
var migrations embed.FS

func RunMigrations(ctx context.Context, driver string, conn *sql.DB) error { /* goose.Provider.Up */ }
```
- 파일명: `NNNNN_description.sql` (5자리 0-padded 순번). 예: `00016_create_notices.sql`.
- 각 파일은 `-- +goose Up` / `-- +goose Down` 섹션.
- goose가 `goose_db_version` 테이블로 적용 버전 추적. `main.go` 기동 시 자동 Up.
- 드라이버: `sqlite`(개발/테스트), `mysql`(운영). `gooseDialect(driver)`로 분기.
- **스키마 변경은 항상 새 마이그레이션 파일로**. 기존 파일 수정 금지.

### 6.3 손으로 쓴 repository vs sqlc

| 방식 | 사용 기준 |
|------|----------|
| **손으로 쓴 repository** (기본) | 대부분의 도메인. 복잡한 동적 쿼리·트랜잭션·soft delete·암호화 필드 |
| **sqlc 생성 코드** (선택) | 단순·정적 CRUD(roles, members 등). `db/queries/*.sql` → `sqlcdb` 패키지 생성 |

손으로 쓴 repository 표준 구조:

```go
var _ domainx.Repository = (*XRepository)(nil)

type XRepository struct{ db *sql.DB }
func NewXRepository(conn *sql.DB) *XRepository { return &XRepository{db: conn} }

const xColumns = "`id`,name,created_at,updated_at,created_by,updated_by"

func (r *XRepository) Save(ctx context.Context, x domainx.X) error {
	if _, err := r.db.ExecContext(ctx, "INSERT INTO `x` ("+xColumns+") VALUES (?,?,?,?,?,?)",
		x.ID, x.Name, x.CreatedAt.Unix(), x.UpdatedAt.Unix(), nullString(x.CreatedBy), nullString(x.UpdatedBy),
	); err != nil {
		return fmt.Errorf("save x: %w", err)
	}
	return nil
}

func (r *XRepository) FindByID(ctx context.Context, id string) (domainx.X, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+xColumns+" FROM `x` WHERE `id`=?", id)
	x, err := scanX(row)
	if errors.Is(err, sql.ErrNoRows) { return domainx.X{}, domainx.ErrNotFound }
	if err != nil { return domainx.X{}, fmt.Errorf("find x: %w", err) }
	return x, nil
}
```

공통 헬퍼(레포 패키지 공유): `rowScanner`(인터페이스, `*sql.Row`/`*sql.Rows` 공용 스캔), `nullString`(빈 문자열→NULL), `checkAffected`(0행→도메인 `ErrNotFound`). **도메인 에러 누출 방지를 위해 `checkAffected`는 도메인별로 별개 구현**(notice vs board처럼).

### 6.4 트랜잭션

`TxManager` 포트로 추상화: `WithinTx(ctx, func(ctx) error) error`. 여러 레포 호출을 원자적으로 묶을 때 유스케이스가 사용. SQLite는 단일 writer라 `conn.SetMaxOpenConns(1)`로 락 충돌 방지.

---

## 7. 설정(Config) 컨벤션

- 파일: `configs/{APP_ENV}.yaml` (`APP_ENV` 미설정 시 `local`).
- 로드: `config.Load()` → YAML 언마샬 → 정규화(URL trim) → `validate()`(필수 필드·양수 검증).
- 비밀(`jwt_secret`, `encryption_secret`, `api_key`)은 YAML/env에만, 코드 기본값 금지.
- 환경 분기: `SeedsInitialAdmin()`처럼 메서드로 캡슐화(`Env == "local"||"dev"||"docker"`).
- `Duration` 커스텀 타입으로 `"10s"` 문자열 파싱.
- 새 외부 서비스 추가 시: Config에 섹션 구조체 추가 → `validate()`에 필수 검증 추가.

```go
type Config struct {
	Env      string         // APP_ENV에서 주입(yaml 아님)
	Server   ServerConfig   `yaml:"server"`
	Security SecurityConfig `yaml:"security"` // cors/csrf
	Database DatabaseConfig `yaml:"database"` // driver/dsn
	Auth     AuthConfig     `yaml:"auth"`     // jwt_secret/expiry/초기계정
	// ...외부 서비스 섹션
}
```

---

## 8. 관리자(Admin) 기능 표준 세트

템플릿이 기본 제공하는 admin 영역. 새 프로젝트는 이 세트를 베이스라인으로 깐다.

### 8.1 멤버 관리 (`/api/v1/members`)
| 메서드·경로 | 최소 역할 | 비고 |
|---|---|---|
| `GET /members` | admin | 목록(role 필터·페이지네이션) |
| `POST /members` | admin | 생성 |
| `PUT /members/{id}/role` | admin | 역할 변경(자신보다 낮은 레벨만 — `CanControl`) |
| `PUT /members/{id}/active` | admin | 활성/비활성 토글 |
| `DELETE /members/{id}` | **super_admin** | 삭제 |
| `GET /members/{id}` | user | 본인/대상 조회 |

### 8.2 전역 설정 (`/api/v1/settings/*`, admin 전용)
- 외부 연동 설정(예: GitLab·Slack): `GET/PUT` + `POST .../test`(연결 테스트).
- 비밀 값은 AES-GCM 암호화 저장, 응답은 마스킹(last-four만).

### 8.3 외부 Provider 관리 (예: AI Providers)
- `GET`(목록/단건)은 user, `POST/PUT/DELETE/ping`은 admin.
- 고정 카테고리 목록은 코드 상수로 제공(`Builtin...Categories`).

### 8.4 콘텐츠/공지 관리
- 조회는 user, 변경(생성/수정/삭제/토글)은 admin.
- HTML 본문은 `Sanitizer` 포트로 XSS 정제 후 저장.

### 8.5 통계/대시보드
- `GET /api/v1/statistics/*` admin 전용 집계.

### 8.6 초기 관리자 시딩
- 비운영 환경(`SeedsInitialAdmin()`)에서만 `seedInitialAdmin`으로 super_admin/user 자동 생성.
- 운영 환경은 시딩 금지(수동 부트스트랩).

---

## 9. 새 도메인 추가 체크리스트

새 어그리거트 `X`를 추가할 때 **이 순서대로** 진행한다(헥사고날 안→밖):

- [ ] **1. `domain/x/model.go`** — 엔티티, 커맨드, sentinel/typed 에러(`ErrNotFound`/`ErrValidation`/`ErrPermission`), `Normalize()`/`Validate()`.
- [ ] **2. `domain/x/port.go`** — `Service`(in), `Repository`(out), 필요한 추가 포트(`Sanitizer` 등).
- [ ] **3. `domain/x/service.go`** — (필요 시) 순수 도메인 로직.
- [ ] **4. `application/x/usecase.go`** — `var _ domainx.Service = ...`, 인가 게이트(`accessGate`), ID/시각/감사필드 채움.
- [ ] **5. 마이그레이션** — `db/.../migrations/NNNNN_create_x.sql`(+ Up/Down). (sqlc 쓰면 `db/queries/x.sql`도.)
- [ ] **6. `adapter/out/db/x_repository.go`** — `var _ domainx.Repository = ...`, `scanX`, `time.Unix` 변환, 0행→`ErrNotFound`.
- [ ] **7. `adapter/in/http/x_handler.go`** — `func(w,r) error` 핸들러, DTO(`xWriteReq`/`xResp`), `xErrToHTTP` 매핑, claims로 actorID.
- [ ] **8. `cmd/api/main.go`** — repo→usecase→handler 와이어링 + 라우트 등록(`router.Public/Secured` + `MinRole`).
- [ ] **9. 검증** — `go build ./...`, `gofmt -l`, `go vet ./...`, 관련 `go test`(테스트 변경은 사용자 승인 후).
- [ ] **10. 문서** — `docs/API-SPEC.md`에 엔드포인트·권한·DTO 추가.

---

## 10. 코드 품질 & 보안 체크리스트 (PR 전 자가검수)

**Go 관용구**
- [ ] `ctx context.Context`가 모든 외부향 함수의 첫 인자.
- [ ] 에러 래핑 `%w`, 비교 `errors.Is/As`. 에러 무시(`_ = err`) 없음.
- [ ] 백그라운드 고루틴은 `context.Background()` 별도 사용 + 에러 반드시 로깅.
- [ ] 슬라이스/맵은 크기 알 때 `make(_, 0, n)`. set은 `map[K]struct{}`.
- [ ] 컴파일 타임 인터페이스 확인 `var _ Iface = (*Impl)(nil)`.

**보안**
- [ ] 토큰/API키/시크릿이 코드에 하드코딩되지 않음(config로만).
- [ ] 로그에 토큰·PII·평문 비밀 없음.
- [ ] 외부 비밀은 DB 저장 시 AES-GCM 암호화, 응답은 마스킹.
- [ ] 인가가 우회 불가(라우트 `MinRole` + 유스케이스 게이트 이중).
- [ ] 외부 입력(ID·IID 등)을 유스케이스/도메인에서 재검증.
- [ ] 외부 API 호출에 타임아웃 설정.
- [ ] HTML 등 신뢰 불가 입력은 `Sanitizer`로 정제.
- [ ] `go vet ./...` / 레이스(`-race`) 통과.

---

## 11. 기술 스택 (go.mod 기준 권장 의존성)

| 용도 | 라이브러리 |
|------|-----------|
| 라우팅 | 표준 `net/http`(Go 1.22+ 메서드·path 패턴) |
| 마이그레이션 | `github.com/pressly/goose/v3` |
| (선택) 쿼리 생성 | `sqlc` |
| UUID | `github.com/google/uuid` |
| MySQL 드라이버 | `github.com/go-sql-driver/mysql` |
| SQLite 드라이버 | `modernc.org/sqlite` (CGO-free) |
| CORS | `github.com/rs/cors` |
| YAML | `gopkg.in/yaml.v3` |
| 로깅 | 표준 `log/slog` |
| JWT | 표준 crypto 기반 또는 검증된 JWT 라이브러리 |

---

## 12. 참조

- 개발 오케스트레이션(전 과정 지휘): `.claude/skills/go-api-orchestrator/SKILL.md`
- 아키텍처/구조 설계: `.claude/skills/go-api-architect/SKILL.md`
- 데이터 레이어(모델·레포지토리·마이그레이션): `.claude/skills/go-api-data/SKILL.md`
- HTTP 레이어·비즈니스 로직(핸들러·라우터·DTO·미들웨어): `.claude/skills/go-api-implementer/SKILL.md`
- 테스트(단위·통합·경계면 검증): `.claude/skills/go-api-test/SKILL.md`
- API 명세 포맷: `docs/API-SPEC.md`
- 프로젝트 규칙: `CLAUDE.md` (테스트 보호 룰 포함)
</content>
</invoke>
