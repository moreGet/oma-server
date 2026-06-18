# 02. 데이터 레이어 구현 요약 (data-engineer)

> 설계 `01_architect_design.md` 를 기준으로 데이터 레이어 + config + logger 구현 완료.
> HTTP 레이어·main.go·application 유스케이스·out/llm(adapter/cache/factory)은 다음 단계(implementer) 담당.
> (이전 버전 요약은 sqlc/구 구조 기준이었으므로 전면 교체함.)

## 1. 생성/수정 파일 목록 (신규 트리 `com/ohmyagent/`)

| 경로 | 구분 |
|---|---|
| `go.mod` | 수정(module aiagent, go 1.25, 설계 §2 의존성) |
| `configs/local.yaml` | 신규 |
| `configs/dev.yaml` | 신규 |
| `configs/docker.yaml` | 신규 |
| `configs/prod.yaml` | 신규 |
| `com/ohmyagent/internal/config/config.go` | 신규 |
| `com/ohmyagent/internal/logger/logger.go` | 신규 |
| `com/ohmyagent/internal/domain/auth/model.go` | 신규 |
| `com/ohmyagent/internal/domain/auth/port.go` | 신규 |
| `com/ohmyagent/internal/domain/auth/service.go` | 신규(LevelForRoleID/NameForRoleID) |
| `com/ohmyagent/internal/domain/llmprovider/model.go` | 신규 |
| `com/ohmyagent/internal/domain/llmprovider/port.go` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/migrations/00001_create_roles.sql` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/migrations/00002_create_members.sql` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/migrations/00003_create_llm_providers.sql` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/db.go` | 신규(Open/gooseDialect) |
| `com/ohmyagent/internal/adapter/out/db/migrate.go` | 신규(embed + RunMigrations) |
| `com/ohmyagent/internal/adapter/out/db/helpers.go` | 신규(rowScanner/nullString/strFromNull) |
| `com/ohmyagent/internal/adapter/out/db/role_repository.go` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/member_repository.go` | 신규 |
| `com/ohmyagent/internal/adapter/out/db/llmprovider_repository.go` | 신규 |
| `com/ohmyagent/internal/adapter/out/auth/bcrypt_hasher.go` | 신규 |

> 기존 파일(`internal/...`, 구 `migrations/`, `config/`)은 설계 지침대로 **삭제하지 않음**.
> implementer 단계 후 일괄 정리. `*_test.go` 미변경.

## 2. 포트 → 구현 매핑

| 포트(도메인) | 구현체(어댑터) | 컴파일 타임 확인 |
|---|---|---|
| `domainauth.RoleRepository` | `db.RoleRepository` | `var _ domainauth.RoleRepository = (*RoleRepository)(nil)` |
| `domainauth.Repository` | `db.MemberRepository` | `var _ domainauth.Repository = (*MemberRepository)(nil)` |
| `domainauth.PasswordHasher` | `authout.BcryptHasher` | `var _ domainauth.PasswordHasher = (*BcryptHasher)(nil)` |
| `domainllmprovider.Repository` | `db.LLMProviderRepository` | `var _ domainllmprovider.Repository = (*LLMProviderRepository)(nil)` |

미구현(implementer 담당):
- `domainauth.Service` → `application/auth` 유스케이스
- `domainauth.TokenService` → `adapter/in/http/security/jwt.go`
- `domainllmprovider.Service` → `application/llmprovider` 유스케이스
- `domainllmprovider.Cache`/`Factory`/`Adapter` → `adapter/out/llm/{cache,factory,*_adapter}.go`

## 3. implementer 가 호출할 핵심 시그니처

### 생성자(main.go 와이어링)
```go
db.Open(driver, dsn string, maxOpenConns int) (*sql.DB, error)   // sqlite 면 MaxOpenConns=1 강제, mysql 은 인자(<=0→10)
db.RunMigrations(ctx context.Context, driver string, conn *sql.DB) error
db.NewRoleRepository(conn *sql.DB) *db.RoleRepository
db.NewMemberRepository(conn *sql.DB) *db.MemberRepository
db.NewLLMProviderRepository(conn *sql.DB) *db.LLMProviderRepository
authout.NewBcryptHasher(cost int) *authout.BcryptHasher          // cost<=0 → bcrypt.DefaultCost
config.Load() (*config.Config, error)                            // APP_ENV → configs/{env}.yaml
logger.Init(logger.Options{Level, Format}) *slog.Logger          // 또는 logger.New(...)
```

### config.Config 필드(유스케이스/main 참조)
```go
cfg.Env                          // local/dev/docker/prod
cfg.ServerAddr() string          // ":8080"
cfg.Server.{Port int, ReadTimeout, WriteTimeout config.Duration}  // .Std() 로 time.Duration
cfg.Security.AllowedOrigins []string
cfg.Database.{Driver, DSN string, MaxOpenConns int}
cfg.Auth.{JWTSecret string, JWTExpiry config.Duration, SeedAdminUsername, SeedAdminPassword string}
cfg.SeedsInitialAdmin() bool     // Env ∈ {local,dev,docker}
```
- `config.Duration` 은 `UnmarshalYAML`("10s"/"24h")을 가지며 `.Std()` 로 `time.Duration` 반환.

### auth Repository (멤버/역할)
```go
// db.MemberRepository (domainauth.Repository) — 모든 메서드 ctx 첫 인자
Save(ctx, m Member) error
Update(ctx, m Member) error                      // role_id/active/updated_at/updated_by 갱신, 0행→ErrNotFound
FindByID(ctx, id string) (Member, error)         // 없으면 ErrNotFound; Role(id/name/level) 을 roles JOIN 으로 채움
FindByUsername(ctx, username string) (Member, error)  // 없으면 ErrNotFound
List(ctx, filter MemberFilter) ([]Member, int, error)  // total 포함; RoleID 0=전체, Limit>0 일 때만 LIMIT/OFFSET 적용
Delete(ctx, id string) error                     // 0행→ErrNotFound

// db.RoleRepository (domainauth.RoleRepository)
FindByID(ctx, id int) (Role, error)              // 없으면 ErrNotFound
List(ctx) ([]Role, error)
```
- 도메인 순수 헬퍼: `auth.LevelForRoleID(roleID int) RoleLevel`, `auth.NameForRoleID(roleID int) string`.
  유스케이스가 `CreateMemberCommand.RoleID` → `Member.Role{ID,Name,Level}` 채울 때 사용 가능
  (또는 `RoleRepository.FindByID` 로 조회해 채워도 됨 — 택일).

### llmprovider Repository
```go
// db.LLMProviderRepository (domainllmprovider.Repository)
GetActive(ctx) (LLMProvider, error)              // 없으면 ErrNoActiveProvider
FindByID(ctx, id string) (LLMProvider, error)    // 없으면 ErrNotFound
List(ctx) ([]LLMProvider, error)
Save(ctx, p LLMProvider) error                   // INSERT (config 는 내부에서 json.Marshal)
UpdateConfig(ctx, id string, cfg ProviderConfig, updatedAt int64, updatedBy string) error  // 0행→ErrNotFound
Activate(ctx, id string, now int64, actorID string) error  // tx: 전체 비활성→지정 활성, 0행→ErrNotFound(롤백)
Delete(ctx, id string) error                     // 0행→ErrNotFound
```

### PasswordHasher
```go
Hash(plain string) (string, error)
Compare(hash, plain string) error                // 불일치 → domainauth.ErrInvalidCredentials
```

## 4. 주의점 / 결정 사항

1. **json 직렬화 위치**: `ProviderConfig` ↔ `config_json(TEXT)` 변환은 **repository 내부**
   (`marshalConfig`/`unmarshalConfig`)에서 수행. 도메인/유스케이스는 JSON 을 모름. 포트 시그니처는
   `ProviderConfig` 구조체를 주고받음(bytes 아님). 빈 config_json 은 zero-value 로 허용.

2. **시각 변환**: 저장 `t.Unix()`, 복원 `time.Unix(n,0).UTC()`.
   - `UpdateConfig.updatedAt`, `Activate.now` 는 **unix seconds(int64)** 인자 → 유스케이스가
     `time.Now().UTC().Truncate(time.Second).Unix()` 로 채워 전달할 것.
   - `Save` 는 `LLMProvider.CreatedAt/UpdatedAt`(time.Time)를 내부에서 `.Unix()` 변환.

3. **goose API 버전**: v3.21 `goose.NewProvider(dialect, conn, fsys)` Provider API 사용.
   - embed `migrations/*.sql` → `fs.Sub(migrations,"migrations")` 로 루트 조정 후 Provider 에 전달.
   - `gooseDialect`: mysql→`goose.DialectMySQL`, sqlite→`goose.DialectSQLite3`.
   - **중요**: `provider.Close()` 는 넘긴 `*sql.DB` 를 닫으므로 `RunMigrations` 안에서 호출하지 않음
     (conn 수명은 main.go 소유). main.go 가 `defer conn.Close()` 담당.

4. **드라이버명 매핑**: `sql.Open` 드라이버명 — mysql→`"mysql"`, sqlite→`"sqlite"`(modernc.org/sqlite 등록명).
   드라이버는 `db.go` 의 blank import 로 등록됨(`_ "github.com/go-sql-driver/mysql"`, `_ "modernc.org/sqlite"`).

5. **members ↔ roles JOIN**: `MemberRepository` 조회는 roles 를 JOIN 해 `Member.Role`(id/name/level)을 채움.
   FK(`role_id`)는 마이그레이션에 정의. 유스케이스에서 새 멤버 생성 시 `Member.Role.ID`(role_id)만 세팅하면
   INSERT 됨(Name/Level 은 저장 안 함, 조회 시 JOIN 으로 복원).

6. **CreateCommand.Validate()** 는 도메인 소유. 유스케이스는 호출 후 `uuid.NewString()` ID,
   `time.Now()` 시각/감사필드를 채워 `Save` 호출(스펙 §3.4 패턴).

7. **env 비밀 주입**: `APP_AUTH_JWT_SECRET`, `APP_DATABASE_DSN`, `APP_AUTH_SEED_ADMIN_PASSWORD`
   환경변수가 YAML 값을 덮어씀. 운영(prod)에서 jwt_secret 비면 `Load()` 가 에러(validate).

8. **Go 미설치 → 빌드 미검증**: 시그니처·import·패키지명 정확성에 주의해 작성. 최초 빌드 시
   goose Provider API(`NewProvider`/`Up`/`Dialect` 상수) 및 `go.sum` 재생성 필요.

## 5. 미해결 / implementer 로 위임

- 샘플 Provider / 초기 admin 시드: app 코드 시딩 위치(`config.SeedsInitialAdmin()` 게이트)는
  유스케이스/main 단계에서 확정(설계 §9-3). `auth.LevelForRoleID`/`NameForRoleID` 로 super_admin Role 구성 가능.
- `EnsureProjectAccess`: 포트 시그니처만 존재(domainauth.Service). 구현은 유스케이스에서 admin↑ 통과로 단순화(설계 §9-6).
- 기존 파일 정리·테스트 재작성: 사용자 승인 후 별도 진행.
