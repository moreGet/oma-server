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

Provider `config` 필드: `endpoint`, `model`, `api_key_env`(시크릿 환경변수 **이름**만 저장), `max_tokens`, `extra_params`.

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
| ProviderType / 모델 | 어댑터 | 채팅 스트리밍 |
|---|---|---|
| EXTERNAL (`claude`* 로 시작) | ClaudeAdapter | ✅ Anthropic Messages API `/v1/messages` (stream) |
| EXTERNAL (그 외) | OpenAIAdapter | ✅ `/v1/chat/completions` (stream) |
| LOCAL | OllamaAdapter | ✅ `/api/chat` (stream) |

> 라우팅: EXTERNAL Provider 의 `config.model` 이 `claude` 로 시작하면 Claude, 아니면 OpenAI.
> Claude 특이사항: `role:"system"` 메시지는 자동으로 top-level `system` 필드로 분리되며,
> `max_tokens` 미지정 시 1024 가 기본 적용된다(Anthropic 필수 필드).

### Anthropic(Claude) 연동 준비
1. `export ANTHROPIC_API_KEY="sk-ant-..."`
2. Provider 생성(admin): `provider_type:"EXTERNAL"`, `config.model:"claude-3-5-sonnet-latest"`(또는 사용할 실제 모델 ID), `config.api_key_env:"ANTHROPIC_API_KEY"`, `is_active:true`
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
