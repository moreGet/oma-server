# API-SPEC

OhMyAgent AI Agent 서버 HTTP API 명세. 모든 경로는 `/api/v1` 프리픽스. 인증은 JWT Bearer.

**에러 envelope (두 종류)**
- **평면**(auth/members/roles/llm-providers/statistics/chat, `/me`·`/me/quota`·`/me/password`): `{ "code": "BAD_REQUEST", "message": "..." }` (스펙 §5.2). 코드: `BAD_REQUEST | UNAUTHORIZED | FORBIDDEN | NOT_FOUND | CONFLICT | TOO_MANY_REQUESTS | BAD_GATEWAY | INTERNAL_ERROR`
- **중첩**(클라이언트 계약: health/models/agent/*, **`/users/me`**, **`/projects/*`**, **`/tools/*`**, **`/client/version`**, **`/security/command-policy`**, **`/chat/rooms*`**·**`/chat/ws`**): `{ "error": { "code": "bad_request", "message": "..." } }`
  소문자 코드: `bad_request | unauthorized | forbidden | not_found | rate_limited | backend_error`

---

## 에러 코드 (전수)

같은 HTTP 상태라도 envelope 종류에 따라 코드 표기가 다르다(평면=대문자, 중첩=소문자). **HTTP 상태로 분기**하는 것을 권장한다.

| HTTP | 평면 code | 중첩 code | 의미 | 대표 발생 상황 |
|---|---|---|---|---|
| **400** | `BAD_REQUEST` | `bad_request` | 잘못된 요청 | JSON 파싱 실패, 필수 필드 누락, 형식 오류(`messages` 빈 배열, `api_key_env` 형식 위반, 첨부 MIME 불허/>10MiB, `client_id`/`title` 누락 등), **요청 본문 과대**(JSON 본문 상한 초과 — 일반 1 MiB, chat/agent·세션·대화 push 32 MiB) |
| **401** | `UNAUTHORIZED` | `unauthorized` | 인증 실패 | Authorization 헤더 없음 / Bearer 토큰 만료·서명불일치·형식오류 |
| **403** | `FORBIDDEN` | `forbidden` | 인가 실패 | 역할 부족(`MinRole` 미달), 타인 소유 리소스 접근(세션/멤버 `CanControl` 위반) |
| **404** | `NOT_FOUND` | `not_found` | 리소스 없음 | 멤버/Provider/프로젝트/대화 없음, **활성 LLM Provider 없음**(`no active llm provider`), 미구현 선택 엔드포인트 |
| **405** | `METHOD_NOT_ALLOWED` | `backend_error` | 미허용 메서드 | 라우터에 없는 메서드 |
| **409** | `CONFLICT` | `backend_error` | 충돌 | 중복(예: username 중복 생성) |
| **429** | `TOO_MANY_REQUESTS` | `rate_limited` | 한도 초과 | **토큰 쿼터(일/주/월) 초과** 또는 **세션 저장 캡 초과**. 인증과 무관 |
| **500** | `INTERNAL_ERROR` | `backend_error` | 서버 오류 | 미처리 예외/패닉(복구되어 500 반환) |
| **502** | `BAD_GATEWAY` | `backend_error` | 업스트림 오류 | LLM 호출 실패, 어댑터가 채팅 미지원(예: 잘못된 설정) |

> 중첩 envelope 매핑은 **HTTP 상태 기준**이다: 400→`bad_request`, 401→`unauthorized`, 403→`forbidden`, 404→`not_found`, 429→`rate_limited`, **그 외(405/409/500/502)→`backend_error`**.

### 429 토큰 쿼터 메시지 (상세)
쿼터 초과 시 `message` 에 **어느 윈도우·사용량·리셋 시각**이 포함된다(클라가 그대로 표시 가능):
```json
{ "error": { "code": "rate_limited",
  "message": "daily token quota exceeded: used 1115 of 5 (period 2026-06-27, resets 2026-06-28T00:00:00Z)" } }
```
- `daily | weekly | monthly` 중 어떤 한도에 걸렸는지 명시. `resets` 는 ISO-8601 UTC(다음 경계: 일=자정 / 주=다음 월요일 00:00 / 월=다음 달 1일).
- 세션 저장 캡 초과는 `message: "session storage limit exceeded"`(429).
- 잔여량은 `GET /api/v1/me/quota` 로 사전 확인 가능.

### 클라이언트 처리 가이드 (중요)
**상태별로 동작을 구분**한다. 특히 **401에서만 재로그인**하고, 그 외는 메시지를 띄울 뿐 세션을 비우지 않는다.

| 상태 | 권장 클라 동작 |
|---|---|
| **401** | 토큰 무효/만료 → **재로그인 유도**(여기서만 세션 폐기) |
| **403** | 권한 부족 → "권한이 없습니다" 안내. **로그아웃 금지** |
| **429** | 쿼터/캡 초과 → **오류 메시지 표시**(message 그대로) + 재시도/대기. **로그아웃 금지** |
| **404** | 리소스 없음/미구현 선택 기능 → graceful(해당 기능만 비활성). **로그아웃 금지** |
| **400** | 입력 오류 → 사용자에게 수정 안내 |
| **500 / 502** | 서버/업스트림 오류 → "잠시 후 다시 시도" + 재시도(backoff) |

> ⚠️ **429/404/403/5xx 를 인증 실패로 오인해 로그인 화면으로 튕기지 말 것.** 토큰이 유효한데도 429(쿼터)·404(미구현) 등으로 로그인 루프가 발생하는 흔한 버그다.

### SSE 스트리밍 중 오류
스트리밍이 **시작된 뒤**(200 + 헤더 전송 후) 발생한 오류는 상태코드를 못 바꾸므로 이벤트로 통지한다:
- `POST /chat`: `data: {"error":"...","done":true}`
- `POST /agent/chat`: `event: error` / `data: {"error":{"code":"backend_error","message":"..."}}`

스트리밍 **시작 전** 오류(검증/활성 Provider 없음/쿼터 등)는 위 표대로 일반 JSON 에러(+상태코드)로 반환된다.

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
| `PUT /api/v1/members/{id}/password` | admin (하위 멤버 비번 리셋, CanControl) |
| `GET /api/v1/members/{id}/tool-policy` | admin (멤버 도구 정책 오버라이드 조회) |
| `PUT /api/v1/members/{id}/tool-policy` | admin (멤버 도구 정책 오버라이드 설정) |
| `DELETE /api/v1/members/{id}` | super_admin |

#### 멤버별 도구 정책 오버라이드 (admin, 평면 envelope)
전역 도구 정책에 **계층 병합**되는 멤버 단위 허용/차단 오버라이드다(모드는 전역 전용이라 멤버는 오버라이드 불가).
- `GET /api/v1/members/{id}/tool-policy` → `{member_id, enabled, disabled, updated_at?, updated_by?}`. 오버라이드 없으면 `enabled`/`disabled` 가 `null`.
- `PUT /api/v1/members/{id}/tool-policy` body `{enabled?:[도구명], disabled?:[도구명]}` → 갱신된 정책(200). **빈 배열/생략 = 오버라이드 해제**(전역만 적용).

**유효 정책 합성(전역 ⊕ 멤버, "전역=보안 하한")** — `GET /api/v1/tools/policy`·`POST /api/v1/tools/authorize` 가 반환·적용하는 실제 정책:
- `mode` = 전역값(멤버 무관).
- `disabled` = **전역 ∪ 멤버**(합집합) — 전역 차단은 항상 적용되고 멤버는 차단을 **추가만** 가능(전역이 막은 도구를 멤버가 다시 열 수 없음).
- `enabled`(화이트리스트) = 둘 다 비면 전체 허용 / 한쪽만 지정 시 그 목록 / **둘 다 지정 시 교집합**(멤버는 허용 범위를 좁히기만 가능).

멤버 프로필 필드(선택): `POST /api/v1/members` 는 `email`·`display_name`·`organization` 을 함께 받을 수 있고, 멤버 응답에도 포함된다(미설정 시 빈 값). 어드민 웹·`GET /api/v1/users/me` 에서 노출. (역할 CanControl: super_admin→admin·user, admin→user.)

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

### 도구 정책 / 클라이언트 버전 / 명령 보안 (user, 선택 기능)
서버 미구현/오류 시 클라는 graceful(정책 없음=전체 허용, 버전 알림 생략, 명령 보안=클라 디폴트만).
- **도구 정책**(`tools/policy`)·**명령 보안**(`security/command-policy`)은 **DB(전역 `tool_policy_settings` + 멤버별 `member_tool_policy`)**, **클라이언트 버전**(`client/version`)은 **DB(`client_version_settings`)** 에 저장되고 어드민(`/admin/tools` 전역, `/admin/members` 멤버별, `/admin/client`)에서 편집한다(즉시 반영, atomic 캐시). yaml 설정 아님.
- **도구 카탈로그**: 클라이언트가 노출하는 26개 도구명은 서버 상수(`domain/toolpolicy` `ClientTools`)이자 DB 시드(`tool_catalog`)로 고정되어, 어드민이 허용/차단을 자유 문자열 대신 **고정 목록(카테고리 리스트)** 에서 고른다(오타 방지).

| 메서드·경로 | 기능 |
|---|---|
| `GET /api/v1/tools/policy` | **인증 멤버에 적용되는 유효** 도구 실행 정책 `{mode, enabled, disabled}`(전역 ⊕ 멤버 오버라이드, 위 §멤버별 도구 정책 합성 규칙). `mode`=`cached`\|`realtime`(그 외 cached 간주), `enabled`=null이면 전체 허용, `disabled` 우선 |
| `POST /api/v1/tools/authorize` | (realtime) 도구 1회 인가. body `{tool, arguments?}` → `{allowed, reason}`. **인증 멤버의 유효 정책** 기준: disabled 우선 → enabled 화이트리스트 → 그 외 허용 |
| `GET /api/v1/client/version` | 클라 버전 점검 `{latest, minimum_supported, download_url?, notice?, mandatory}`(SemVer). 클라가 자기 버전과 비교해 업데이트 알림 |
| `GET /api/v1/security/command-policy` | 서버 추가 위험명령/경로 차단 패턴 `{blocked_patterns[], blocked_paths[]}`. 클라 내장 디폴트에 **추가만**(2중 안전). 미설정 시 빈 배열 |

> `cached` 모드면 정책은 **로그인 시 1회** 로드(세션 캐시). `enabled`/`disabled`는 nil이면 응답에서 `null`(=전체 허용). `download_url`/`notice`는 빈 값이면 응답에서 생략. 상세 계약은 클라 `docs/server-tool-policy-api.md`·`server-version-api.md`·`server-controlled-security-and-tools.md` 참조.

#### `GET /api/v1/security/command-policy` (user) — 서버 제어형 위험명령 차단
**2중 안전 원칙**: 클라이언트 내장 디폴트 블랙리스트는 항상 적용되고, 서버는 패턴을 **추가만** 한다(끄는 필드 없음). 로그인 시 1회 로드·세션 캐시. 미구현/오프라인이면 클라 디폴트만으로 정상 동작.

응답 200 (중첩 envelope 경로):
```json
{
  "blocked_patterns": [
    { "type": "regex",     "pattern": "\\bnet\\s+user\\b", "reason": "사용자 계정 조작 금지", "script_type": "any" },
    { "type": "substring", "pattern": "bcdedit",            "reason": "부트 설정 변조 금지",   "script_type": "powershell" }
  ],
  "blocked_paths": [
    { "type": "substring", "pattern": "D:\\\\sensitive", "reason": "민감 디렉토리 접근 금지" }
  ]
}
```

| 필드 | 값 | 설명 |
|---|---|---|
| `type` | `regex` \| `substring` | 매칭 방식. 서버가 정규화(미지정/이상값 → `substring` 안전 기본) |
| `pattern` | string | 패턴(빈 값은 응답에서 제외) |
| `reason` | string | 차단 사유(표시/로그). 빈 값이면 생략 |
| `script_type` | `any` \| `powershell` \| `cmd` | 적용 셸(`blocked_patterns`만). 미지정/이상값 → `any` |

- **설정**: 어드민 `/admin/tools`(DB `tool_policy_settings`). 비우면 `{"blocked_patterns":[],"blocked_paths":[]}`.
- 서버는 도구 끄는 필드를 두지 않는다(디폴트 약화 불가) — 2중 안전.

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
| `GET /api/v1/me/quota` | user | 본인 토큰 쿼터 현황(일/주/월 한도·사용·잔여). 아래 [토큰 쿼터](#토큰-쿼터사용자별-일주월-한도) 참조 |
| `PUT /api/v1/me/password` | user | 본인 비밀번호 변경 `{old_password,new_password}` |
| `GET /api/v1/roles` | user | 역할 목록(드롭다운) |
| `POST /api/v1/llm-providers/{id}/test` | admin | Provider 연결 테스트(1토큰 ping) |
| `GET /api/v1/statistics` | admin | 대시보드 집계 `{members:{total,by_role}, providers:{total,active}}` |

### 어드민 웹 페이지 (`/admin`)
- **스택**: 서버사이드 렌더링 `html/template` + **Bootstrap 5.3(다크 `data-bs-theme`)** + **Bootstrap Icons**(CDN). 사이드바 레이아웃, 생성/관리는 **모달**. Node 빌드 불필요, Go 바이너리에 `go:embed`.
- **인증**: 로그인 시 JWT 를 **HttpOnly·SameSite=Lax 쿠키**(`admin_session`)에 저장. 페이지는 쿠키로 인증(API 의 Bearer 와 독립).
- **페이지**: `/admin/login`, `/admin/`(대시보드 통계), `/admin/members`(목록 + 생성/관리 모달: 프로필·역할·활성·비번리셋·**토큰 한도(일/주/월)**·사용량 초기화·**세션 한도**·**도구 정책 오버라이드(기본/허용/차단 카테고리 리스트)**·삭제 + 전역 기본 토큰 한도), `/admin/providers`(목록 + 등록/관리 모달), `/admin/transcripts`(대화 이력 저장: 백엔드 DB/파일/S3·보존·첨부 스트립·연결테스트), `/admin/sessions`(세션 저장: 백엔드 DB/파일/S3·전역 최대 세션 수·연결테스트), `/admin/tools`(**전역 도구 정책**: 모드(cached/realtime)·허용/차단 도구(**카탈로그 카테고리 리스트**: 기본/허용/차단 3-상태)·위험명령/경로 차단 패턴(JSON), DB 저장·즉시 반영. 멤버별 오버라이드는 `/admin/members` 모달의 '도구' 탭), `/admin/client`(**클라이언트 버전**: latest·minimum_supported·download_url·notice·mandatory, DB 저장·즉시 반영 → `GET /api/v1/client/version` 에 반영), `/admin/chat`(**채팅 관리/모더레이션**: 방·메시지·첨부 집계 + 방 목록 + 방 상세(멤버·메시지 검토) + 메시지 소프트삭제·방 삭제, 삭제 시 멤버에게 실시간 반영), `/admin/account`(계정 정보 + 본인 프로필 편집). 비밀번호 변경 UI는 멤버 관리로 통합(셀프 변경은 API `/me/password`). 사이드바는 섹션별 접이식(슬라이드) 메뉴(개요/사용자/AI/저장소/채팅/보안·도구/클라이언트).

### 토큰 쿼터(사용자별 일·주·월 한도)
- **모델**: **일(YYYY-MM-DD)·주(YYYY-Www, ISO)·월(YYYY-MM)** 3개 기간 한도를 동시 시행(UTC, 자동 리셋). 한도 = 윈도우별 **멤버 값(>0) 우선, 없으면 전역 기본값**, 0이면 그 윈도우 무제한. 카운트=`total_tokens`.
- **시행(소프트)**: chat/agent 스트리밍 **시작 전** 일·주·월 중 하나라도 사용량 ≥ 한도면 **429**(`TOO_MANY_REQUESTS` / agent 계약 `rate_limited`). 응답 **종료 후** 세 기간 카운터에 실제 토큰 누적.
- **사용량 출처 + 폴백**: provider 응답 `usage.total_tokens`(SSE 종료 시 캡처). usage 미제공(0)이면 **토크나이저 추정 폴백** `quota.EstimateTokens`(BPE 의존성 없는 문자 클래스 휴리스틱: CJK ~1토큰/자, 그 외 ~4자/토큰)로 prompt+response 추정 → 카운트 누락 방지.
- **LB-safe 카운터**: 사용량/한도 전부 DB(`token_usage`(member,period)·`member_token_limits`·`quota_config`, 세 기간 키 포맷이 달라 한 테이블 공존). 누적은 driver별 atomic upsert(mysql `ON DUPLICATE KEY`/sqlite `ON CONFLICT`) → 다중 인스턴스 정확.

#### `GET /api/v1/me/quota`  (user) — 본인 잔여량 조회
본인의 일·주·월 한도/사용/잔여를 반환한다(flat envelope). `windows`는 항상 **일→주→월** 순.

응답 200:
```json
{
  "windows": [
    { "window": "day",   "period": "2026-06-27", "limit": 1000, "used": 250,
      "remaining": 750, "unlimited": false, "percent_used": 25.0, "percent_remaining": 75.0 },
    { "window": "week",  "period": "2026-W26",   "limit": 5000, "used": 1200,
      "remaining": 3800, "unlimited": false, "percent_used": 24.0, "percent_remaining": 76.0 },
    { "window": "month", "period": "2026-06",    "limit": 0,    "used": 8400,
      "remaining": 0,    "unlimited": true,  "percent_used": 0.0, "percent_remaining": 100.0 }
  ]
}
```

| 필드 | 타입 | 설명 |
|---|---|---|
| `window` | string | `day` \| `week` \| `month` |
| `period` | string | 현재 기간 키(일=`YYYY-MM-DD`, 주=`YYYY-Www` ISO, 월=`YYYY-MM`, UTC) |
| `limit` | int | 적용 한도(토큰). **0 = 무제한** |
| `used` | int | 이번 기간 누적 사용 토큰 |
| `remaining` | int | `max(0, limit-used)`. 무제한이면 0(`unlimited`로 구분) |
| `unlimited` | bool | `limit=0` |
| `percent_used` / `percent_remaining` | float | 사용률/잔여율 0~100(소수 1자리). 무제한이면 0 / 100 |

> `remaining`이 0인 윈도우가 하나라도 있으면 다음 chat/agent 요청이 429로 거부된다(소프트 시행). 에러: `401`(토큰 무효).

- **어드민 설정**: `/admin/members` 에서 전역 기본(일/주/월) + 멤버별(일/주/월) 한도 설정, 이번 일·주·월 사용량 표시 + 멤버별 사용량 초기화.

### 프로젝트/대화 동기화 (클라이언트 server-api-spec)
모두 **user↑, 중첩 envelope**, 소유권(owner) 스코프, 시각은 ISO-8601 UTC.

| 메서드·경로 | 기능 |
|---|---|
| `GET /api/v1/projects` | 본인 프로젝트 목록 `{projects:[{id,name,created_utc,updated_utc,conversation_count}]}` |
| `POST /api/v1/projects` | 프로젝트 생성/업서트 `{client_id,name}` → `{id,name,created_utc,updated_utc,...}`(201) |
| `GET /api/v1/projects/{id}` | 단건 + 대화 요약 `{id,name,conversations:[{id,client_id,title,updated_utc,message_count}]}` |
| `DELETE /api/v1/projects/{id}` | 프로젝트 삭제(소속 대화 메타 정리) |
| `POST /api/v1/projects/{id}/conversations` | 대화 업서트(push) `{client_id,title,created_utc,updated_utc,messages[]}` → `{id,client_id,updated_utc}` |
| `DELETE /api/v1/projects/{id}/conversations/{cid}` | 대화 삭제 |

- **업서트**: `client_id`(클라 GUID) ↔ 서버 id 매핑, 재전송 시 같은 id 반환. 메타데이터는 DB(`projects`·`conversations`), 소유권(owner) 스코프.
- **대화 본문 저장**: messages 를 **gzip** 후 선택형 백엔드(**DB BLOB / 로컬 디렉터리 / S3**)에 `<owner>/<project>/<conversation>.json.gz` 구조로 저장. 어드민 `/admin/sessions` 에서 백엔드 선택(로그 저장과 동일 방식, 설정은 분리). S3 시크릿 AES-GCM.
- **계정별 세션 캡(하드)**: 신규 대화 세션 수가 한도 도달 시 **429**(`rate_limited`) 거부(기존 세션 업서트는 허용). 한도 = 멤버별 오버라이드(>0) 우선, 없으면 전역 기본(`session_settings.default_max_sessions`), 0=무제한. 어드민 `/admin/sessions`(전역) + 멤버 모달(개별).

### 실시간 채팅 — 사용자 간 메시징 (user, 중첩 envelope, 멤버십 스코프)
**LLM `POST /chat` 과는 별개**의 **사람↔사람** 채팅이다. **단체(group)·1:1(direct)** 방을 지원하고, 메시지는 **RDB 영속**(이력 조회 가능), 실시간 전파는 **WebSocket + 인메모리 허브**(단일 인스턴스 브로드캐스트). 모든 REST/WS 는 **방 멤버만** 접근(비멤버 403).

| 메서드·경로 | 기능 |
|---|---|
| `GET /api/v1/chat/ws` | **WebSocket 업그레이드**. Bearer 헤더로 인증(쿼리 토큰 미지원 — C# 클라이언트는 헤더 가능). 연결 후 송수신 |
| `GET /api/v1/chat/rooms` | 본인이 속한 방 목록 `{rooms:[{id,type,name?,created_at,unread_count}]}` (최근 활동순, 방별 **안읽음 수** 포함) |
| `POST /api/v1/chat/rooms` | 단체 방 생성 `{name, member_ids[]}` → `{id,type:"group",name,created_at,unread_count}`(201). 생성자 자동 포함 |
| `POST /api/v1/chat/rooms/direct` | 1:1 방 가져오기/생성 `{user_id}` → 방(200). 정준 키로 **중복 생성 방지**(이미 있으면 그 방 반환). 자기 자신 400 |
| `GET /api/v1/chat/rooms/{id}/messages?limit=&before=` | 메시지 이력(최신순) `{messages:[{id,room_id,sender_id,content,created_at,edited_at?,deleted?}]}`. `limit`(기본 50, 최대 200), `before`=메시지 id(그 이전 페이지). 삭제 메시지는 순서 유지 위해 포함하되 `content:""`·`deleted:true`. **REST 응답 DTO 는 `mentions`/`attachments` 를 싣지 않는다**(현재는 WS `message` 이벤트 DTO 에만 포함 — 아래 참조) |
| `POST /api/v1/chat/rooms/{id}/messages` | REST 로 메시지 전송 `{content, mentions?:[memberId], attachments?:[{file_name,content_type,size_bytes,url}]}` → 메시지(201). 멘션은 방 멤버로 검증(비멤버 제거), 첨부는 먼저 `POST /chat/attachments` 로 업로드한 뒤 그 메타데이터를 동봉. 본문·첨부 둘 다 없으면 400 |
| `PATCH /api/v1/chat/rooms/{id}/messages/{mid}` | **본인 메시지 수정** `{content}` → 메시지(200, `edited_at` 기록). 남의 메시지 403, 삭제된 메시지 404 |
| `DELETE /api/v1/chat/rooms/{id}/messages/{mid}` | **본인 메시지 삭제**(소프트, 204). content 비우고 `deleted` 표시. 남의 메시지 403. 재삭제 idempotent |
| `POST /api/v1/chat/rooms/{id}/read` | 방을 **지금까지 읽음 처리** → `{room_id,last_read_at}`(200). 읽음 위치는 **단조 증가**(뒤로 안 감) + 방 멤버에게 WS `read` 이벤트 브로드캐스트 |
| `GET /api/v1/chat/rooms/{id}/reads` | 멤버별 **읽음 위치**(읽음 표시 렌더용) `{reads:[{member_id,last_read_at}]}`. 메시지는 `member.last_read_at >= message.created_at` 이면 그 멤버가 읽은 것 |
| `GET /api/v1/chat/unread` | 총/방별 안읽음 배지 `{total, rooms:{<roomId>:<count>}}` (count>0 만 포함) |
| `GET /api/v1/chat/rooms/{id}/members` | 방 멤버 목록 `{members:[<memberId>]}` |
| `POST /api/v1/chat/rooms/{id}/members` | **단체 방에 멤버 추가** `{member_ids[]}` → 갱신된 `{members[]}`(200). **group 한정**(1:1 → 400), 멤버만, 이미 멤버는 무시. 추가 멤버는 가입 시점부터 안읽음 카운트(이전 메시지 제외) |
| `DELETE /api/v1/chat/rooms/{id}/members/{mid}` | **강퇴**(204) — **방 생성자(creator)만**, group 한정. 본인 강퇴 400(→leave), 비멤버 대상 403, 비생성자 403. 강퇴 대상 포함 멤버에게 `member_left` 브로드캐스트 |
| `POST /api/v1/chat/rooms/{id}/leave` | **본인이 방에서 나가기**(204). **group 한정**(1:1 → 400) |
| `GET /api/v1/chat/rooms/{id}/presence` | 방 멤버 중 **온라인** 목록 `{online:[memberId]}` |
| `GET /api/v1/chat/mentions?limit=` | **나를 멘션한** 최신 메시지(삭제 제외) `{messages:[...]}` — 알림 피드 |
| `POST /api/v1/chat/attachments` | **파일 업로드**(`multipart/form-data`, 파트명 `file`) → 첨부 메타데이터 `{id,file_name,content_type,size_bytes,url}`(201). 최대 **10 MiB**, 초과 400. 반환 `url` 을 메시지의 `attachments[].url` 로 사용 |
| `GET /api/v1/chat/attachments/{aid}` | **파일 다운로드**(바이너리). `Content-Type`/`Content-Disposition`(파일명)/`Content-Length`/`X-Content-Type-Options: nosniff` 헤더 포함. 인증 필요(불투명 UUID) |

**WebSocket 프로토콜** (텍스트 프레임, JSON)
- **클라 → 서버**(전송):
  - 메시지: `{"type":"send","room_id":"<id>","content":"<text>"}` — 비멤버/빈 내용 등 실패 시 **그 연결로만** 오류 통지 `{"type":"error","error":"..."}`(브로드캐스트 안 함).
  - 타이핑: `{"type":"typing","room_id":"<id>","state":"start"|"stop"}` — **휘발성**(저장 안 함). 클라가 입력 시작/중단을 디바운스해 전송.
  - 메시지 전송에 멘션/첨부 동봉 가능: `{"type":"send","room_id","content","mentions":[...],"attachments":[...]}` (REST 와 동일 의미).
- **서버 → 클라**(수신):
  - 메시지: `{"type":"message","message":{"id","room_id","sender_id","content","created_at","edited_at"?,"deleted"?,"mentions"?,"attachments"?}}` — **발신자 포함** 방 멤버 전원에게 전파(다기기 일관성). WS DTO 는 `mentions`/`attachments`(둘 다 omitempty)도 싣는다(REST 이력 응답과 달리). `edited_at`/`deleted` 는 비어 있으면 생략.
  - 메시지 수정/삭제: `{"type":"message_edited"|"message_deleted","message":{...,"edited_at"?,"deleted"?}}` — 수정/삭제 시 방 멤버에게 전파(클라가 해당 메시지 갱신/"삭제된 메시지" 표시).
  - 읽음: `{"type":"read","read":{"room_id","member_id","last_read_at"}}` — 누군가 `POST .../read` 하면 방 멤버에게 전파(읽음 표시 실시간 갱신).
  - 타이핑: `{"type":"typing","typing":{"room_id","member_id","state"}}` — **발신자 제외** 방 멤버에게 전파(저장·이력 없음).
  - 멤버 변경: `{"type":"member_joined"|"member_left","member":{"room_id","member_id"}}` — 단체 방 멤버 추가/나가기/강퇴 시 방 멤버에게 전파(클라가 멤버 목록 갱신).
  - 온라인 상태: `{"type":"presence","presence":{"member_id","online"}}` — 멤버의 첫 연결(online)/마지막 연결 해제(offline) 시 **같은 방을 공유하는 멤버들**에게 전파.
- keepalive: 서버가 주기적 **ping**, 클라는 pong 응답(미응답 시 연결 종료). 한 사용자가 여러 기기/탭으로 다중 연결 가능.

> 시각은 unix epoch(초). 방/메시지/읽음위치는 `chat_rooms`·`chat_room_members`(`last_read_at`)·`chat_messages`(`edited_at`/`deleted_at`/`mentions`/`attachments`) 테이블. **첨부 바이너리는 `chat_attachments`(BLOB) 테이블**에 저장(메타데이터 + 바이너리). 향후 파일/S3 백엔드로 교체 가능하도록 `AttachmentStore` 포트로 추상화. **안읽음** = 내 `last_read_at` 이후 **남이 보낸**(삭제 제외) 메시지 수. **온라인 상태(presence)는 메모리(허브)에만** 있어 저장하지 않는다 — 재기동/오프라인 시 사라짐.
>
> **다중 인스턴스(LB)**: `config` 의 `messaging.broadcaster` 로 전환한다 — `memory`(기본, 단일 인스턴스) | `redis`(다중 인스턴스 **Redis pub/sub**). `redis` 면 메시지·수정/삭제·읽음·타이핑·멤버 변경 이벤트가 인스턴스 간 실시간 전파된다. (단, presence 온라인 **스냅샷 조회**(`GET .../presence`)와 전환 감지는 인스턴스별 로컬 연결 기준 — 이벤트는 전파되나 조회는 그 인스턴스에 붙은 연결만 반영. 완전한 분산 presence 는 후속 과제.) 이력/안읽음/멘션/첨부는 DB 라 어느 인스턴스에서나 조회 가능.

### 대화 이력 / 감사 로깅
- **감사 로깅**: 모든 slog 이벤트가 **비동기 핸들러**(버퍼+워커)로 비차단 기록. `event` taxonomy: `auth.*`/`member.*`/`provider.*`/`chat.request`/`agent.request`(메타데이터만, 본문·시크릿 미기록). 헬스 제외, `request_id` 상관관계.
- **대화 이력**: chat/agent SSE 종료 시 요청+응답을 **gzip 압축** 후 비차단 저장. 백엔드는 어드민 `/admin/transcripts`에서 선택 — **DB(gzip BLOB)** / **로컬 파일** / **S3(minio-go)**. S3 시크릿은 **AES-GCM 암호화**(`APP_ENCRYPTION_SECRET`), 응답 마스킹. 끄면 기록 생략.
- **액션 피드백**: 작업 결과는 **토스트**(성공=초록/오류=빨강)로 노출. 플래시는 쿠키에 base64 인코딩(한글 보존) + 성공/오류 레벨 구분.
- **권한 UI 게이팅**: USER 는 읽기 전용(멤버 메뉴 숨김, Provider 변경 버튼 숨김). 백엔드 use case 가 이중으로 인가 강제. 멤버 생성/역할 드롭다운은 actor 가 제어 가능한 역할만 노출.
- 같은 오리진이라 CORS 불필요. (CSRF 는 SameSite=Lax 로 1차 완화; 토큰 기반 CSRF 는 후속.)
