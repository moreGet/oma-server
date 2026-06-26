# API-SPEC

OhMyAgent AI Agent 서버 HTTP API 명세. 모든 경로는 `/api/v1` 프리픽스. 인증은 JWT Bearer.

**에러 envelope (두 종류)**
- 관리 API(auth/members/llm-providers/chat): 평면 `{ "code": "BAD_REQUEST", "message": "..." }` (스펙 §5.2)
- **C# 에이전트 클라이언트 계약 API**(health/models/agent/*): 중첩 `{ "error": { "code": "bad_request", "message": "..." } }`
  소문자 코드: `bad_request | unauthorized | forbidden | not_found | rate_limited | backend_error`

## 인증

| 메서드·경로 | 권한 | 설명 |
|---|---|---|
| `POST /api/v1/auth/login` | Public | `{username, password}` → `{token, ...}` |

## 멤버 관리 (§8.1)

| 메서드·경로 | 최소 역할 |
|---|---|
| `GET /api/v1/members` | admin |
| `POST /api/v1/members` | admin |
| `GET /api/v1/members/{id}` | user (본인 또는 admin↑) |
| `PUT /api/v1/members/{id}/role` | admin |
| `PUT /api/v1/members/{id}/active` | admin |
| `DELETE /api/v1/members/{id}` | super_admin |

## LLM Provider 관리

| 메서드·경로 | 최소 역할 |
|---|---|
| `GET /api/v1/llm-providers` | user |
| `GET /api/v1/llm-providers/{id}` | user |
| `POST /api/v1/llm-providers` | admin |
| `PATCH /api/v1/llm-providers/{id}/config` | admin |
| `PUT /api/v1/llm-providers/{id}/activate` | admin |
| `DELETE /api/v1/llm-providers/{id}` | admin |

Provider `config` 필드: `endpoint`, `model`, `max_tokens`, `extra_params`, 그리고 API 키는 **둘 중 하나**:
- `api_key_env`: 시크릿 환경변수 **이름**만 저장(예: `OPENAI_API_KEY`). 환경변수 이름 형식만 허용(키 값 직접 입력 시 400).
- `api_key`: 키 값 **직접 등록**(입력 전용 평문). 서버가 **AES-GCM 암호화**하여 DB 저장하며(`APP_ENCRYPTION_SECRET` 필요, 미설정 시 400), **응답에는 절대 노출하지 않고** `api_key_set: true/false`(마스킹)로만 표시. 설정 수정 시 `api_key`를 비우면 기존 키를 보존.

---

## 질의 (Chat) — 클라이언트 → 활성 LLM → SSE 응답

### `POST /api/v1/chat`  (최소 역할: user)

클라이언트(C# 등)가 대화 메시지를 보내면, 서버가 **활성 Provider**의 LLM(OpenAI/Ollama 등)으로 전달하고
응답을 **SSE(Server-Sent Events) 스트리밍**으로 되돌려준다.

**요청 헤더**: `Authorization: Bearer <token>`, `Content-Type: application/json`

**요청 바디**
```json
{
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "안녕? 너는 누구야?"}
  ],
  "model": "gpt-4o-mini",   // (선택) 활성 Provider 기본 모델 오버라이드
  "max_tokens": 1024,        // (선택) 0/생략 = 미지정
  "temperature": 0.7         // (선택) 0~2
}
```
- `role` ∈ `system | user | assistant`. `messages` 비어 있으면 400.

**응답** (`Content-Type: text/event-stream`)

각 이벤트는 `data: {json}\n\n` 형식:
```
data: {"delta":"안","done":false}

data: {"delta":"녕하세요","done":false}

data: {"done":true,"finish_reason":"stop","usage":{"prompt_tokens":23,"completion_tokens":8,"total_tokens":31}}
```
- 증분: `{"delta": "...", "done": false}`
- 종료: `{"done": true, "finish_reason": "...", "usage": {...}}`
- 스트리밍 중 오류: `{"error": "...", "done": true}`

**스트리밍 시작 전 오류**는 일반 JSON 에러로 반환:
- 입력 검증 실패 → `400 BAD_REQUEST`
- 활성 Provider 없음 → `404 NOT_FOUND` (`no active llm provider`)
- 어댑터가 채팅 미지원(예: Claude) → `502 BAD_GATEWAY`
- 외부 LLM 호출 실패 → `502 BAD_GATEWAY`

### Provider별 채팅 지원 현황
어댑터는 모두 **각 벤더 공식 Go SDK**를 사용한다(직접 HTTP 호출 아님).

| ProviderType / 모델 | 어댑터 | 공식 SDK | 채팅 스트리밍 |
|---|---|---|---|
| EXTERNAL (`claude`* 로 시작) | ClaudeAdapter | `github.com/anthropics/anthropic-sdk-go` | ✅ Messages API (stream, tool use) |
| EXTERNAL (`gemini`* 로 시작) | GeminiAdapter | `google.golang.org/genai` | ✅ GenerateContentStream (function calling) |
| EXTERNAL (그 외) | OpenAIAdapter | `github.com/openai/openai-go/v3` | ✅ Chat Completions (stream, function calling) |
| LOCAL | OllamaAdapter | `github.com/ollama/ollama/api` | ✅ Chat (stream, tools) |

> 라우팅: EXTERNAL Provider 의 `config.model` 접두사로 분기한다 — `claude*`→Claude, `gemini*`→Gemini, 그 외→OpenAI.
> Claude 특이사항: `role:"system"` 메시지는 자동으로 top-level `system` 필드로 분리되며,
> `max_tokens` 미지정 시 1024 가 기본 적용된다(Anthropic 필수 필드).
> Gemini 특이사항: `system`→`SystemInstruction`, `assistant`→role `model`, 도구 결과는 function-response 파트로 user 턴에 실린다(전용 tool 역할 없음).

### Anthropic(Claude) 연동 준비
1. `export ANTHROPIC_API_KEY="sk-ant-..."`
2. Provider 생성(admin): `provider_type:"EXTERNAL"`, `config.model:"claude-3-5-sonnet-latest"`(또는 사용할 실제 모델 ID), `config.api_key_env:"ANTHROPIC_API_KEY"`, `is_active:true`
3. `POST /api/v1/chat` 로 질의(SSE 동일 포맷).

### Google(Gemini) 연동 준비
1. `export GEMINI_API_KEY="..."` (Google AI Studio 발급 키)
2. Provider 생성(admin): `provider_type:"EXTERNAL"`, `config.model:"gemini-1.5-flash"`(또는 `gemini-2.0-flash` 등), `config.api_key_env:"GEMINI_API_KEY"`, `is_active:true`
3. `POST /api/v1/chat` 로 질의(SSE 동일 포맷). 모델명이 `gemini` 로 시작하므로 GeminiAdapter 로 라우팅된다.

### Local(Ollama) 연동 준비
1. 로컬에 Ollama 실행(`ollama serve`, 기본 `http://localhost:11434`) 후 모델 풀: `ollama pull llama3`
2. Provider 생성(admin): `provider_type:"LOCAL"`, `config.model:"llama3"`, `config.endpoint:"http://localhost:11434"`(미설정 시 `OLLAMA_HOST`→기본값), `is_active:true`. **API 키 불필요.**
3. `POST /api/v1/chat` 로 질의(SSE 동일 포맷).

### OpenAI 연동 준비
1. API 키를 환경변수로 설정: `export OPENAI_API_KEY="sk-..."`
2. OpenAI Provider 생성(admin):
   ```bash
   curl -X POST localhost:8080/api/v1/llm-providers \
     -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     -d '{"name":"openai","provider_type":"EXTERNAL","is_active":true,
          "config":{"model":"gpt-4o-mini","api_key_env":"OPENAI_API_KEY"}}'
   ```
3. (필요 시) 활성화: `PUT /api/v1/llm-providers/{id}/activate`
4. 질의:
   ```bash
   curl -N -X POST localhost:8080/api/v1/chat \
     -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     -d '{"messages":[{"role":"user","content":"hello"}]}'
   ```
   (`-N` = 버퍼링 비활성, 스트리밍 확인용)

---

## C# 에이전트 클라이언트 계약 (API_CONTRACT)

인증: `Authorization: Bearer <token>` (JWT, `/auth/login` 으로 발급). health 만 인증 불필요.
에러는 위의 **중첩 envelope** 사용.

### `GET /api/v1/health`  (Public)
서버 연결/헬스 체크. 항상 200.
```json
{ "status": "ok", "time": "2026-06-21T09:00:00Z", "database": "ok" }
```
`status`: `ok|degraded`, `database`: `ok|down|skipped`.

### `GET /api/v1/models`  (user)
등록된 Provider 기반 모델 목록(모델 선택 칩).
```json
{ "models": [ { "id": "gpt-4o-mini", "name": "openai", "provider_type": "EXTERNAL", "active": true } ] }
```
`id` 를 `/agent/chat` 의 `model` 로 전달.

### `POST /api/v1/agent/chat`  (user) — 에이전트 루프의 심장
대화기록 + 도구 스키마를 전달하고 SSE 로 텍스트·도구호출을 수신한다. 서버는 도구를 실행하지 않고
백엔드 LLM function-calling 으로 중계만 한다(stateless). 클라이언트가 도구를 실행해 `tool` 메시지로 재요청.

**요청**
```json
{
  "messages": [
    {"role": "system", "content": "You are a coding agent."},
    {"role": "user", "content": "list files"},
    {"role": "assistant", "content": "", "tool_calls": [{"id":"c1","name":"list_dir","arguments":"{\"path\":\".\"}"}]},
    {"role": "tool", "tool_call_id": "c1", "content": "main.go\nREADME.md"}
  ],
  "tools": [
    {"name":"list_dir","description":"List directory","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
  ],
  "model": "gpt-4o-mini",
  "max_tokens": 2048,
  "temperature": 0.2,
  "metadata": {"os": "windows", "workspace_root": "C:\\proj"}
}
```
- `messages[].attachments[]`: `{file_name, content_type, size_bytes, data_base64}` (요구 D). 텍스트 계열(`text/*`,`application/json` 등)은 본문에 인라인, 그 외(이미지/PDF)는 메타 노트만. 파일당 ≤10MiB, 허용 MIME 외엔 400.

**응답** (SSE, named events)
```
event: message_start
data: {"role":"assistant","model":"gpt-4o-mini"}

event: content_delta
data: {"delta":"Sure, "}

event: tool_call
data: {"id":"call_abc","name":"read_file","arguments":"{\"path\":\"main.go\"}"}

event: message_stop
data: {"stop_reason":"tool_use","usage":{"prompt_tokens":52,"completion_tokens":17,"total_tokens":69}}
```
- `stop_reason` ∈ `end_turn | tool_use | max_tokens` (제공자 finish_reason 을 이 어휘로 정규화).
  → `tool_use` 면 클라이언트가 도구 실행 후 `tool` 메시지로 재요청(루프 지속), `end_turn` 이면 종료.
- 스트리밍 중 오류: `event: error` `data: {"error":{"code":"backend_error","message":"..."}}`.
- function-calling 지원: OpenAI(`tools`), Claude(Anthropic `tools`/`tool_use`), Ollama(모델 의존, best-effort).

### `GET /api/v1/agent/suggestions?workspace_root=`  (user)
동작 힌트 카드(요구 G). 현재 stub:
```json
{ "suggestions": [] }
```
(`{text, prompt?, icon?}` 배열. 비면 클라이언트가 UI 자동 숨김.)

### 채팅 세션 동기화 (user, 소유권 스코프)
| 메서드·경로 | 기능 |
|---|---|
| `GET /api/v1/agent/sessions` | 요약 목록 `{sessions:[{id,title,created_at,updated_at}]}` |
| `GET /api/v1/agent/sessions/{id}` | 단건 `{id,title,data,created_at,updated_at}` (`data`=클라 히스토리 JSON, 서버 불투명) |
| `PUT /api/v1/agent/sessions/{id}` | upsert. body `{title, data}`. 타인 소유 ID → 403 |
| `DELETE /api/v1/agent/sessions/{id}` | 삭제(204) |

> 서버는 세션 `data` 를 불투명 JSON 으로 보관(소유자·시각만 관리). 클라이언트가 로컬 영속 대신/병행 사용 가능.

---

## 관리자(Admin) 기능

### 권한 모델 (요구 3)
| 역할 | level | 제어 범위 |
|---|---|---|
| super_admin | 2 | admin·user 제어 + 멤버 삭제 + 전역 |
| admin | 1 | user 제어, 멤버/Provider 관리 |
| user | 0 | **읽기 전용**(본인 계정·Provider/모델 조회·질의 API 사용) |

상위만 하위를 제어(`RoleLevel.CanControl`). 멤버 삭제는 super_admin 전용.

### 기본 super_admin 시딩
서버 기동 시 super_admin 이 **하나도 없으면 항상 생성**한다(모든 환경). 비밀번호는 `APP_AUTH_SEED_ADMIN_PASSWORD` 우선,
미설정 시 **랜덤 생성 후 기동 로그에 1회 경고 출력**(로그인 후 즉시 변경 권장). 사용자명은 `APP_AUTH_SEED_ADMIN_USERNAME`(기본 `admin`).

### 추가 관리 JSON API
| 메서드·경로 | 최소 역할 | 기능 |
|---|---|---|
| `GET /api/v1/me` | user | 현재 로그인 사용자 |
| `GET /api/v1/users/me` | user | 클라 프로필 카드 `{username, display_name, organization, email}`(중첩 envelope; members 컬럼 기반, display_name 빈 값이면 username 폴백, org/email 빈 값이면 null) |
| `PUT /api/v1/me/password` | user | 본인 비밀번호 변경 `{old_password,new_password}` |
| `GET /api/v1/roles` | user | 역할 목록(드롭다운) |
| `PUT /api/v1/members/{id}/password` | admin | 하위 멤버 비밀번호 리셋 `{new_password}` (CanControl) |
| `POST /api/v1/llm-providers/{id}/test` | admin | Provider 연결 테스트(1토큰 ping) |
| `GET /api/v1/statistics` | admin | 대시보드 집계 `{members:{total,by_role}, providers:{total,active}}` |

### 어드민 웹 페이지 (`/admin`)
- **스택**: 서버사이드 렌더링 `html/template` + **Bootstrap 5.3(다크 `data-bs-theme`)** + **Bootstrap Icons**(CDN). 사이드바 레이아웃, 생성/관리는 **모달**. Node 빌드 불필요, Go 바이너리에 `go:embed`.
- **인증**: 로그인 시 JWT 를 **HttpOnly·SameSite=Lax 쿠키**(`admin_session`)에 저장. 페이지는 쿠키로 인증(API 의 Bearer 와 독립).
- **페이지**: `/admin/login`, `/admin/`(대시보드 통계), `/admin/members`(목록 + 생성/관리 모달 + **토큰 한도**), `/admin/providers`(목록 + 등록/관리 모달), `/admin/transcripts`(대화 이력 저장 설정: 활성화·백엔드 DB/파일/S3·연결테스트), `/admin/account`(계정 정보). 비밀번호 변경 UI는 멤버 관리로 통합(셀프 변경은 API `/me/password`).

### 토큰 쿼터(사용자별 일·주·월 한도)
- **모델**: **일(YYYY-MM-DD)·주(YYYY-Www, ISO)·월(YYYY-MM)** 3개 기간 한도를 동시 시행(UTC, 자동 리셋). 한도 = 윈도우별 **멤버 값(>0) 우선, 없으면 전역 기본값**, 0이면 그 윈도우 무제한. 카운트=`total_tokens`.
- **시행(소프트)**: chat/agent 스트리밍 **시작 전** 일·주·월 중 하나라도 사용량 ≥ 한도면 **429**(`TOO_MANY_REQUESTS` / agent 계약 `rate_limited`). 응답 **종료 후** 세 기간 카운터에 실제 토큰 누적.
- **사용량 출처 + 폴백**: provider 응답 `usage.total_tokens`(SSE 종료 시 캡처). usage 미제공(0)이면 **토크나이저 추정 폴백** `quota.EstimateTokens`(BPE 의존성 없는 문자 클래스 휴리스틱: CJK ~1토큰/자, 그 외 ~4자/토큰)로 prompt+response 추정 → 카운트 누락 방지.
- **잔여량 조회 API**: `GET /api/v1/me/quota`(user↑) → 본인 일/주/월 `{window, period, limit, used, remaining, unlimited, percent_used, percent_remaining}` 배열. (flat envelope)
- **LB-safe**: 사용량/한도 전부 DB(`token_usage`(member,period)·`member_token_limits`·`quota_config`, 세 기간 키 포맷이 달라 한 테이블 공존). 누적은 driver별 atomic upsert(mysql `ON DUPLICATE KEY`/sqlite `ON CONFLICT`) → 다중 인스턴스 정확.
- **어드민**: `/admin/members` 에서 전역 기본(일/주/월) + 멤버별(일/주/월) 한도 설정, 이번 일·주·월 사용량 표시.

### 프로젝트/대화 동기화 (클라이언트 server-api-spec)
- **엔드포인트**(user↑, 중첩 envelope): `GET/POST /api/v1/projects`, `GET/DELETE /api/v1/projects/{id}`, `POST /api/v1/projects/{id}/conversations`, `DELETE /api/v1/projects/{id}/conversations/{cid}`. 시각은 ISO-8601 UTC.
- **업서트**: `client_id`(클라 GUID) ↔ 서버 id 매핑, 재전송 시 같은 id 반환. 메타데이터는 DB(`projects`·`conversations`), 소유권(owner) 스코프.
- **대화 본문 저장**: messages 를 **gzip** 후 선택형 백엔드(**DB BLOB / 로컬 디렉터리 / S3**)에 `<owner>/<project>/<conversation>.json.gz` 구조로 저장. 어드민 `/admin/sessions` 에서 백엔드 선택(로그 저장과 동일 방식, 설정은 분리). S3 시크릿 AES-GCM.
- **계정별 세션 캡(하드)**: 신규 대화 세션 수가 한도 도달 시 **429**(`rate_limited`) 거부(기존 세션 업서트는 허용). 한도 = 멤버별 오버라이드(>0) 우선, 없으면 전역 기본(`session_settings.default_max_sessions`), 0=무제한. 어드민 `/admin/sessions`(전역) + 멤버 모달(개별).

### 대화 이력 / 감사 로깅
- **감사 로깅**: 모든 slog 이벤트가 **비동기 핸들러**(버퍼+워커)로 비차단 기록. `event` taxonomy: `auth.*`/`member.*`/`provider.*`/`chat.request`/`agent.request`(메타데이터만, 본문·시크릿 미기록). 헬스 제외, `request_id` 상관관계.
- **대화 이력**: chat/agent SSE 종료 시 요청+응답을 **gzip 압축** 후 비차단 저장. 백엔드는 어드민 `/admin/transcripts`에서 선택 — **DB(gzip BLOB)** / **로컬 파일** / **S3(minio-go)**. S3 시크릿은 **AES-GCM 암호화**(`APP_ENCRYPTION_SECRET`), 응답 마스킹. 끄면 기록 생략.
- **액션 피드백**: 작업 결과는 **토스트**(성공=초록/오류=빨강)로 노출. 플래시는 쿠키에 base64 인코딩(한글 보존) + 성공/오류 레벨 구분.
- **권한 UI 게이팅**: USER 는 읽기 전용(멤버 메뉴 숨김, Provider 변경 버튼 숨김). 백엔드 use case 가 이중으로 인가 강제. 멤버 생성/역할 드롭다운은 actor 가 제어 가능한 역할만 노출.
- 같은 오리진이라 CORS 불필요. (CSRF 는 SameSite=Lax 로 1차 완화; 토큰 기반 CSRF 는 후속.)
