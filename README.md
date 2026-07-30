# OhMyAgent · AI Agent Server

Go로 작성한 AI 에이전트 백엔드. 멤버/권한 관리, LLM Provider 관리, 채팅·에이전트 SSE 스트리밍 API, **사용자 간 실시간 채팅(WebSocket, 단체/1:1)**, **에이전트 레지스트리 + A2A 토큰 브로커**, **서비스 계정(헤드리스 비대화형 인증)**, 서버사이드 렌더링 어드민 콘솔(`/admin`)을 제공한다.

- **언어/런타임**: Go (표준 `net/http` + `log/slog`)
- **DB / 마이그레이션**: SQLite(modernc, 순수 Go) 또는 MySQL · [goose](https://github.com/pressly/goose) 임베드 마이그레이션
- **인증**: JWT(HS256) Bearer · bcrypt 비밀번호 해시 · **서비스 계정 장수 API 키**(`oma_sa_` 접두사, SHA-256 해시 저장)
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
      out/messagingbus/     # 실시간 채팅 브로드캐스터(memory | redis pub/sub)
      out/transcript/       # 대화 이력 저장(db | file | s3) + 비동기 기록기
      out/sessionstore/     # 세션 본문 저장(db | file | s3)
      out/crypto/           # AES-GCM(시크릿 암호화) · ES256(A2A 토큰 서명)
      out/auth/             # bcrypt 해셔
    config/                 # configs/{APP_ENV}.yaml 로드·검증
    logger/                 # slog 설정 + 비동기(버퍼+워커) 핸들러
configs/                    # local/dev/docker/prod.yaml
cicd/nginx.conf             # 리버스 프록시(응답 gzip, SSE·WS 압축/버퍼링 제외)
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
server:
  port: 8080
  read_timeout: "120s"                    # 본문 전체 수신까지 포함(agent/chat 은 최대 32MiB). slowloris 는 ReadHeaderTimeout 이 따로 막는다
  write_timeout: "10s"
security:
  allowed_origins: []                     # 비면 CORS 비활성(로컬)
  encryption_secret: "local-dev-only-encryption-key-change-me"  # API 키 DB 직접 저장용(AES-GCM). 운영은 env(APP_ENCRYPTION_SECRET) override
database:
  driver: "sqlite"
  dsn: "file:~/aiagent.db?_pragma=busy_timeout(5000)"  # ~ 는 홈으로 확장. /mnt/c 등 Windows 마운트의 sqlite I/O 이슈 회피
  max_open_conns: 1
auth:
  jwt_secret: "local-dev-only-jwt-secret-change-me"  # 로컬 편의 고정값(재시작해도 로그인 유지). 운영은 env(APP_AUTH_JWT_SECRET) 주입 필수
  jwt_expiry: "24h"
  seed_admin_username: "admin"
  seed_admin_password: "admin"            # 비운영 편의값(운영은 env 주입)
messaging:
  broadcaster: "memory"                   # memory(단일 인스턴스) | redis(다중 인스턴스 pub/sub)
  redis:
    addr: "localhost:6379"                # broadcaster=redis 일 때 접속 주소
    password: ""                          # 운영은 env(APP_MESSAGING_REDIS_PASSWORD) override
registry:                                 # 에이전트 레지스트리(등록·발견·생존성·A2A 브로커)
  heartbeat_interval: "15s"               # register 응답으로 내려가 헤드리스 루프 주기를 정함
  lease_ttl: "45s"                        # 권장 3×interval. online <45s ≤ stale <135s ≤ offline
  token_ttl: "120s"                       # A2A 브로커 토큰 수명
  sweep_interval: "5m"                    # offline 24h+ 방치 레코드 정리 주기("0s"=비활성)
```
> sqlite DSN 의 `~`/`~/` 는 config 로더가 사용자 홈으로 확장한다(`expandHomePath`).

MySQL 로 띄우려면 레포 루트의 `docker-compose.yml`(MariaDB 10.11)을 쓰면 된다:
```bash
docker compose up -d mariadb
APP_ENV=docker APP_DATABASE_DSN='ohmyagent:ohmyagent@tcp(localhost:3306)/ohmyagent?parseTime=true' \
  APP_AUTH_JWT_SECRET=dev-secret go run ./com/ohmyagent/cmd/api
```
> `configs/docker.yaml` 은 driver=mysql 이고 DSN·JWT 시크릿을 **env 주입 전제**로 비워 둔다.

### 4) 실행
```bash
APP_ENV=local go run ./com/ohmyagent/cmd/api
# → :8080 리슨, SQLite 마이그레이션 적용, super_admin 시딩
```

### 5) 어드민 콘솔
브라우저에서 **`http://localhost:8080/admin/login`** → `admin` / `admin`(로컬 기본) 로 로그인.
대시보드·멤버 관리(역할·토큰/세션 한도·**멤버별 도구 정책 오버라이드**)·**Provider 관리(admin↑ 전용 — 비관리자는 Provider 메뉴 숨김·페이지 접근 시 대시보드로 리다이렉트)**·대화 이력·세션 저장·**도구 정책(`/admin/tools`, 전역 + 카탈로그 카테고리 리스트)**·**클라이언트 버전(`/admin/client`)**·**에이전트 레지스트리(`/admin/agents`, 생존성·소유자 표시·강제 해제)**·**채팅 관리/모더레이션(`/admin/chat`)**·내 계정 페이지 제공(사이드바는 섹션별 접이식 메뉴).

> 서비스 계정은 어드민 콘솔 페이지가 없고 **API 전용**이다(`/api/v1/service-accounts*`, admin↑).

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
| `APP_MESSAGING_REDIS_PASSWORD` | 채팅 redis 브로드캐스터 비밀번호(`messaging.broadcaster: redis` 일 때, yaml보다 우선) |
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

## 에이전트 레지스트리 · A2A 토큰 브로커

헤드리스 에이전트가 자신을 **등록**하고 서로를 **발견**하며, 에이전트 간 호출용 **단수명 토큰**을 발급받는 경로다.

- **생존성**: `heartbeat_interval`(15s)마다 하트비트. 마지막 하트비트 기준 `lease_ttl`(45s) 이내 `online`, 3×`lease_ttl`(135s) 이내 `stale`, 그 이후 `offline`. 발견 API 기본값은 `online+stale`(offline 은닉).
- **정리**: `sweep_interval`(5m)마다 offline 로 24h 이상 방치된 레코드를 삭제해 무한 축적을 막는다.
- **A2A 토큰**: `POST /agents/{id}/token` 이 `token_ttl`(120s) 짜리 **ES256** 서명 토큰을 발급한다. 공개키는 `GET /agents/a2a-public-key`. ES256(ECDSA P-256)을 쓰는 이유는 Go stdlib 과 .NET BCL 양쪽에서 외부 의존성 없이 검증 가능하기 때문(Ed25519 는 .NET BCL 미지원).
- 서명 개인키는 AES-GCM 으로 암호화해 저장한다(`APP_ENCRYPTION_SECRET`).

## 서비스 계정 (헤드리스 비대화형 인증)

사람 로그인 없이 도는 에이전트/배치를 위한 장수 API 키. 어드민(admin↑)이 발급·폐기한다.

- 키 형식은 `oma_sa_` 접두사의 불투명 문자열. 서버는 **SHA-256 해시만** 저장하며 평문은 **발급 응답에서 단 한 번만** 반환한다.
- `Authorization: Bearer oma_sa_...` 로 인증한다. 접두사가 붙은 요청만 키 조회 경로를 타므로 **기존 JWT 경로는 그대로**다.
- 인증된 요청은 `user` 레벨로 고정(최소권한). 계정 ID 가 `member_id` 자리에 들어가 쿼터·도구 정책이 자연스럽게 적용된다.
- **키 최소 수명 90일**(서버 강제): `expires_at` 을 지정하면 발급 시점 기준 90일 이상 미래여야 한다(무기한은 예외). 초단기 키는 헤드리스가 조기 401 로 죽는 원인이라 발급 시점에 막는다.

---

## 성능 / 동시성 (대규모 동접)

수천~수만 동접을 견디도록 N/W IO·풀을 튜닝했다.

- **HTTP 서버**: keep-alive `IdleTimeout`(120s)·`ReadHeaderTimeout`(10s, slowloris 완화). SSE 핸들러는 write deadline 을 해제해 장시간 스트리밍 유지.
- **요청 본문 상한**(메모리 보호): 모든 JSON 엔드포인트가 `http.MaxBytesReader` 로 본문 크기를 제한해 디코딩 전 과대 본문을 차단한다(힙 폭증·GC 압력·OOM 방지). 상한은 일반 1 MiB, 첨부 base64 인라인·대화/세션 본문(chat/agent·세션 upsert·대화 push)은 32 MiB. 초과 시 `400`. 첨부 업로드(`multipart`)는 별도로 10 MiB.
- **요청 본문 gzip 수용**(전송량 절감): `Content-Encoding: gzip` 을 받으면 본문 디코더가 **스트리밍 해제**한다. 에이전트는 도구 호출마다 대화 전문을 다시 보내고 그 이력에 소스 원문이 들어 있어 압축이 특히 잘 듣는다(클라이언트 실측 76~80% 감소). 헤더가 없으면 종전과 동일(하위 호환). zip bomb 방어로 위 상한이 **압축 전·해제 후 양쪽**에 걸리고, 초과 시 `413`·미지원 인코딩은 `415`. 응답 압축은 nginx 담당이며 **SSE 는 반드시 제외**한다(압축 버퍼가 차야 나가서 토큰 실시간성이 깨진다) — `cicd/nginx.conf`.
- **첨부 다운로드 청크 스트리밍**: BLOB 을 통째로 읽지 않고 `substr` 로 1 MiB씩 지연 조회한다. 점유 메모리가 **파일 크기와 무관**해져 느린 클라이언트가 전송 내내 전체 파일을 붙잡는 경로(대용량 첨부 + 다수 동시 다운로드 → OOM)를 제거한다. 메타데이터 조회에서 `data` 컬럼을 제외하는 것도 함께.
- **타이핑 인디케이터**: 키 입력마다 오는 고빈도 신호라 이벤트당 DB 2회(`IsMember`+`Members`)를 짧은 TTL 멤버 캐시(5s, 방 4096개 상한)로 **0회**까지 줄였다. 인가가 걸린 경로(전송·조회)는 캐시를 타지 않고 계속 DB 로 검사한다 — 캐시된 멤버십으로 인가를 판단하면 강퇴 후에도 잠시 접근이 열린다.
- **목록 화면의 곁다리 조회 스코프**: 어드민 멤버 페이지는 한 화면(100명)만 렌더하면서 쿼터·세션 한도·도구 정책을 테이블 **전량** 읽고 있었다. 표시 대상 ID로 범위를 좁혀 보유량이 전체 멤버 수가 아니라 페이지 크기에 묶이게 했다(도구 정책 스냅샷 실측 3.5 MiB → 13 KiB).
- **집계는 DB 에서**: 대시보드·`GET /statistics` 의 역할별 인원은 멤버 행을 전량 적재해 메모리에서 세지 않고 `GROUP BY` 한 번으로 끝낸다(실측, 멤버 1만: 40.3ms·9.3 MB → 0.58ms·776 B).
- **N+1 제거**: 서비스 계정 목록이 계정마다 키를 따로 조회하던 것(1+N 쿼리)을 `IN` 절 배치 1회로 묶었다(계정 50개 기준 51쿼리 → 2쿼리).
- **SSE 쓰기 핫패스**: chat/agent 스트리밍의 이벤트 프레이밍(`data:`/`event:`)을 `fmt.Fprintf`(리플렉션 포맷 + 포맷 문자열 할당) 대신 **`sync.Pool` 버퍼**에 직접 조립해 1회 `Write` 한다 — 토큰당 할당을 제거(고동접 스트리밍 GC 압력 완화). 토큰별 flush 는 유지(저지연).
- **DB 풀**: `MaxIdleConns` 를 `MaxOpenConns` 와 동일하게(`database/sql` 기본값 2 대신) 설정해 부하 시 커넥션 open/close churn 을 제거하고, `ConnMaxLifetime`(30m)·`ConnMaxIdleTime`(5m)로 스테일 커넥션을 정리한다. sqlite 는 단일 writer 라 1, mysql 풀 크기는 `max_open_conns`(설정 미지정 시 10)로 조정.
- **채팅 멘션 피드 인덱스**: `GET /chat/mentions` 는 방 필터 없이 `created_at DESC` 정렬+LIMIT 하므로 `chat_messages(created_at)` 인덱스로 전체 스캔+filesort 를 제거(인덱스 순서 스캔 + 조기 LIMIT 종료).
- **멤버 목록 인덱스**: 어드민 `GET /admin/members` 는 `created_at DESC` 정렬+LIMIT/OFFSET 하므로 `idx_members_created_at`(`members(created_at)`, 마이그레이션 00022) 로 테이블 전체 filesort 를 제거(멤버 수가 커져도 인덱스 순서 스캔).
- **토큰 쿼터 핫패스**(대규모 동접 채팅): chat/agent 요청마다 일·주·월 사용량을 윈도우별 개별 조회 대신 **IN 절 단일 쿼리**(`UsageForPeriods`)로 묶고(시행 `Check`·조회 `/me/quota`), 응답 후 누적도 **멀티로우 upsert 단일 쿼리**(`AddUsage`)로 묶어 채팅당 쿼터 DB 왕복을 절반 이하로 줄인다. 무제한(한도 0) 윈도우는 조회 자체를 생략. 토큰 추정 폴백 텍스트(대화 전체 연결)는 **usage 미제공 시에만 지연 생성**(정상 경로 할당 회피). 전역 기본 한도는 **30s TTL 캐시**(`atomic.Pointer`)로 요청당 반복 조회를 없애고, 어드민이 기본 한도를 바꾸면 캐시를 즉시 무효화한다. 스트림 종료 후 사용량 누적은 `context.WithoutCancel` 로 분리해 **클라 조기 종료 시에도 회계 누락이 없다**.
- **LLM 업스트림**: 모든 외부 어댑터가 **공유 HTTP 클라이언트**(Transport `MaxIdleConns=256`·`MaxIdleConnsPerHost=64`, HTTP/2)로 OpenAI/Claude/Gemini 커넥션을 재사용(기본 2 병목 제거). 전역 타임아웃 없이 ctx 로 취소(SSE 장기 스트리밍 보존).
- **활성 Provider 캐시**: 질의마다 DB 조회 없이 `atomic.Value` 캐시에서 활성 Provider 해석.
- **할당 절감(GC)**: 대화 이력·세션 본문 gzip 저장 경로가 `gzip.Writer` 를 **`sync.Pool`** 로 재사용해 요청당 압축기 재할당을 제거(고동접 GC 압력 완화). 응답 본문 이력 저장은 비차단 비동기 큐.
- **로깅**: 헬스 체크 제외(LB 폴링 노이즈), `request_id`(`X-Request-Id`) 상관관계, 상태/지연 기반 레벨, 응답 바이트·클라이언트 IP 포함. slog 이벤트는 버퍼+워커 **비동기 핸들러**로 비차단 기록.

> 수평 확장: 상태는 DB 에만 있고 핸들러는 stateless(JWT)라 인스턴스를 늘려 LB 뒤에 두면 된다. sqlite 는 단일 노드용이므로 다중 인스턴스는 **mysql** 사용. **사용자 간 실시간 채팅**은 인메모리 허브라 다중 인스턴스에선 `messaging.broadcaster: redis`(Redis pub/sub)로 전환해 인스턴스 간 이벤트를 전파한다(기본 `memory`=단일 인스턴스). Redis publish 에는 **2s 타임아웃**을 둬 느린 Redis 가 요청·WS 고루틴을 무기한 붙잡지 못하게 한다.

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
go test ./...
```

> `gofmt -l .` 은 현재 **거의 모든 파일을 나열한다** — 작업 트리가 CRLF 개행이라(Windows 체크아웃) gofmt 가 전부 미포맷으로 본다. 포맷 회귀를 보려면 CRLF 를 제거하고 비교한다:
> ```bash
> tr -d '\r' < FILE.go | gofmt -d /dev/stdin
> ```

---

## 문서

- 📑 [HTTP API 명세 — `docs/API-SPEC.md`](./docs/API-SPEC.md)
- 🏗 [아키텍처 규약 — `TEMPLATE-SPEC.md`](./TEMPLATE-SPEC.md)
- 🤖 [에이전트/하네스 가이드 — `CLAUDE.md`](./CLAUDE.md)
