# OhMyAgent · AI Agent Server

Go로 작성한 AI 에이전트 백엔드. 멤버/권한 관리, LLM Provider 관리, 채팅·에이전트 SSE 스트리밍 API, 서버사이드 렌더링 어드민 콘솔(`/admin`)을 제공한다.

- **언어/런타임**: Go (표준 `net/http` + `log/slog`)
- **DB / 마이그레이션**: SQLite(modernc, 순수 Go) 또는 MySQL · [goose](https://github.com/pressly/goose) 임베드 마이그레이션
- **인증**: JWT(HS256) Bearer · bcrypt 비밀번호 해시
- **LLM 연동**: OpenAI / Anthropic Claude / Google Gemini / Ollama(Local) — 각 벤더 **공식 Go SDK**
- **어드민 UI**: 서버사이드 `html/template` + Bootstrap 5(다크) · `go:embed`, Node 빌드 불필요
- **아키텍처**: 헥사고날(domain → application → adapter). 상세 규약은 [`TEMPLATE-SPEC.md`](./TEMPLATE-SPEC.md)

> 📑 **HTTP API 전체 명세는 [`docs/API-SPEC.md`](./docs/API-SPEC.md) 참고.**

---

## 디렉터리 구조

```
com/ohmyagent/
  cmd/api/                  # 조립 루트(main): config→adapter→usecase→handler→route→serve
  internal/
    domain/                 # 엔티티·포트·순수 로직 (auth, llmprovider, ...)
    application/            # 유스케이스 (포트 조합)
    adapter/
      in/http/              # HTTP 핸들러·DTO·에러매핑 + security(JWT/RBAC/미들웨어)
      in/web/               # 어드민 콘솔(html/template + Bootstrap, 쿠키 인증)
      out/db/               # 레포지토리(손작성 SQL) + goose 마이그레이션
      out/llm/              # LLM 어댑터(OpenAI/Claude/Gemini/Ollama) + 팩토리/캐시
    config/                 # configs/{APP_ENV}.yaml 로드·검증
configs/                    # local/dev/docker/prod.yaml
docs/API-SPEC.md            # HTTP API 명세
TEMPLATE-SPEC.md            # 아키텍처 규약(개발 기준 문서)
```

---

## 빠른 시작

### 1) 요구사항
- Go 1.26+
- (선택) Ollama — 로컬 LLM 사용 시

### 2) 의존성
```bash
go mod tidy
```
LLM 공식 SDK(`openai-go/v3`, `anthropic-sdk-go`, `google.golang.org/genai`, `ollama/ollama`)가 함께 받아진다.

### 3) 설정
`configs/{APP_ENV}.yaml`을 읽는다(`APP_ENV` 미설정 시 `local`). 기본 `configs/local.yaml`:
```yaml
server: { port: 8080, read_timeout: "10s", write_timeout: "10s" }
security:
  allowed_origins: []                     # 비면 CORS 비활성(로컬)
  encryption_secret: "..."                # API 키 DB 직접 저장용(AES-GCM). 운영은 env(APP_ENCRYPTION_SECRET) override
database:
  driver: "sqlite"
  dsn: "file:~/aiagent.db?_pragma=busy_timeout(5000)"  # ~ 는 홈으로 확장. /mnt/c 등 Windows 마운트의 sqlite I/O 이슈 회피
  max_open_conns: 1
auth:
  jwt_secret: ""                          # 운영은 env(APP_AUTH_JWT_SECRET) 필수
  jwt_expiry: "24h"
  seed_admin_username: "admin"
  seed_admin_password: "admin"            # 비운영 편의값(운영은 env 주입)
```
> sqlite DSN 의 `~`/`~/` 는 config 로더가 사용자 홈으로 확장한다(`expandHomePath`).

### 4) 실행
```bash
APP_ENV=local go run ./com/ohmyagent/cmd/api
# → :8080 리슨, SQLite 마이그레이션 적용, super_admin 시딩
```

### 5) 어드민 콘솔
브라우저에서 **`http://localhost:8080/admin/login`** → `admin` / `admin`(로컬 기본) 로 로그인.
대시보드·멤버 관리·Provider 관리·내 계정 페이지 제공.

> ⚠️ `/admin`(끝 슬래시 없음)은 매칭되지 않을 수 있으니 `/admin/login` 또는 `/admin/`으로 접속.

---

## 초기 관리자(super_admin) 시딩

기동 시 super_admin이 하나도 없으면 자동 생성한다.
- **사용자명**: `auth.seed_admin_username`(yaml) 또는 기본 `admin`
- **비밀번호**: `APP_AUTH_SEED_ADMIN_PASSWORD`(env) > `auth.seed_admin_password`(yaml) > **랜덤 생성**(기동 로그에 1회 `WARN`으로 출력 — 로그인 후 즉시 변경 권장)
- 이미 super_admin이 있으면 시딩을 건너뛴다(로그 없음).

---

## 환경변수

| 변수 | 설명 |
|---|---|
| `APP_ENV` | `local`(기본) / `dev` / `docker` / `prod` — 읽을 yaml 선택 |
| `APP_AUTH_JWT_SECRET` | JWT 서명 비밀키(**운영 필수**, yaml보다 우선) |
| `APP_DATABASE_DSN` | DB DSN 오버라이드 |
| `APP_AUTH_SEED_ADMIN_PASSWORD` | 시드 admin 비밀번호 |
| `APP_DB_RESET` | `1`/`true`/`yes`/`on`이면 기동 시 **DB 전체 drop & 재생성**(데이터 삭제). 기본 off. **운영(prod)에서는 무시** |
| `APP_ENCRYPTION_SECRET` | Provider API 키를 DB에 **직접 저장**할 때 AES-GCM 암호화 키 소스. yaml `security.encryption_secret`(모든 환경 기본값 제공)보다 **우선**. **운영은 반드시 고유 값으로 override**(예: `openssl rand -base64 32`) |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY` | 각 LLM Provider API 키(Provider의 `api_key_env`에 변수명만 등록) |
| `OLLAMA_HOST` | Ollama 엔드포인트(미설정 시 `http://localhost:11434`) |

### DB 초기화(옵트인)
개발 중 깨끗한 스키마 + 시드 재생성이 필요할 때만:
```bash
APP_DB_RESET=1 APP_ENV=local go run ./com/ohmyagent/cmd/api
```
> 기본값은 **데이터 보존**이다. `APP_DB_RESET`은 모든 테이블을 drop 후 재생성하므로 멤버·Provider·세션이 전부 삭제된다. 운영에서는 켜도 동작하지 않는다.

---

## LLM Provider

어드민 `/admin/providers`에서 등록한다. **종류**를 고르면 endpoint·API 키 환경변수명·기본 모델이 자동 입력된다(수정 가능). EXTERNAL은 모델명 접두사로 어댑터가 정해진다.

| 종류 | type | 모델 예 | api_key_env | 라우팅 |
|---|---|---|---|---|
| OpenAI | EXTERNAL | `gpt-4o-mini` | `OPENAI_API_KEY` | 기본(접두사 없음) |
| Anthropic Claude | EXTERNAL | `claude-3-5-sonnet-latest` | `ANTHROPIC_API_KEY` | `claude*` |
| Google Gemini | EXTERNAL | `gemini-1.5-flash` | `GEMINI_API_KEY` | `gemini*` |
| Ollama(Local) | LOCAL | `llama3` | — | LOCAL |

API 키는 **둘 중 하나**로 등록:
- **① 환경변수명**(`api_key_env`): 서버 env에 키를 두고 Provider엔 변수명(`OPENAI_API_KEY`)만. 추가 설정 불필요.
- **② 직접 저장**(`api_key`): 키 값을 입력하면 서버가 **AES-GCM 암호화**하여 DB 저장. 응답엔 `api_key_set`(마스킹)만 노출하고 원문은 절대 반환하지 않음. `security.encryption_secret`(env `APP_ENCRYPTION_SECRET`) 필요 — 기본값이 모든 환경에 제공되어 로컬은 바로 동작.

질의는 `POST /api/v1/chat`(SSE), 에이전트 루프는 `POST /api/v1/agent/chat`(SSE, function-calling). 상세는 [`docs/API-SPEC.md`](./docs/API-SPEC.md).

---

## 성능 / 동시성 (대규모 동접)

수천~수만 동접을 견디도록 N/W IO·풀을 튜닝했다.

- **HTTP 서버**: keep-alive `IdleTimeout`(120s)·`ReadHeaderTimeout`(10s, slowloris 완화). SSE 핸들러는 write deadline 을 해제해 장시간 스트리밍 유지.
- **DB 풀**: `MaxIdleConns = MaxOpenConns`(기본 2 → 부하 시 커넥션 churn 제거), `ConnMaxLifetime`(30m)·`ConnMaxIdleTime`(5m)로 스테일 커넥션 정리. mysql 풀 크기는 `max_open_conns`(prod 기본 20)로 조정.
- **토큰 쿼터 핫패스**: chat/agent 요청마다 일·주·월 사용량을 윈도우별 개별 조회 대신 **IN 절 단일 쿼리**(`UsageForPeriods`)로 묶어 DB 왕복을 줄인다(시행 `Check`·조회 `/me/quota` 공통). 무제한(한도 0) 윈도우는 조회 자체를 생략.
- **LLM 업스트림**: 모든 외부 어댑터가 **공유 HTTP 클라이언트**(Transport `MaxIdleConns=256`·`MaxIdleConnsPerHost=64`, HTTP/2)로 OpenAI/Claude/Gemini 커넥션을 재사용(기본 2 병목 제거). 전역 타임아웃 없이 ctx 로 취소(SSE 장기 스트리밍 보존).
- **활성 Provider 캐시**: 질의마다 DB 조회 없이 `atomic.Value` 캐시에서 활성 Provider 해석.
- **할당 절감(GC)**: 대화 이력·세션 본문 gzip 저장 경로가 `gzip.Writer` 를 **`sync.Pool`** 로 재사용해 요청당 압축기 재할당을 제거(고동접 GC 압력 완화). 응답 본문 이력 저장은 비차단 비동기 큐.
- **로깅**: 헬스 체크 제외(LB 폴링 노이즈), `request_id`(`X-Request-Id`) 상관관계, 상태/지연 기반 레벨, 응답 바이트·클라이언트 IP 포함. slog 이벤트는 버퍼+워커 **비동기 핸들러**로 비차단 기록.

> 수평 확장: 상태는 DB 에만 있고 핸들러는 stateless(JWT)라 인스턴스를 늘려 LB 뒤에 두면 된다. sqlite 는 단일 노드용이므로 다중 인스턴스는 **mysql** 사용.

---

## 테스트

```bash
go test ./...                  # 전체
go test ./... -cover           # 패키지별 커버리지
go test ./... -coverprofile=cover.out && go tool cover -func=cover.out   # 함수별/총합
go tool cover -html=cover.out  # 라인별 HTML
```

## 빌드 / 배포 검증

```bash
go build ./...
go vet ./...
gofmt -l .                     # 비어 있어야 함
go test ./...
```

---

## 문서

- 📑 [HTTP API 명세 — `docs/API-SPEC.md`](./docs/API-SPEC.md)
- 🏗 [아키텍처 규약 — `TEMPLATE-SPEC.md`](./TEMPLATE-SPEC.md)
- 🤖 [에이전트/하네스 가이드 — `CLAUDE.md`](./CLAUDE.md)
