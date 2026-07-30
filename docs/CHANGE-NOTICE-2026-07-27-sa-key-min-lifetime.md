# [서버 → 클라이언트] 계약 변경 통지 — 서비스 계정 키 최소 수명 90일 강제

- **일자:** 2026-07-27
- **대상 문서:** `oma-client/docs/server-service-account-api.md` §2B, 서버 `docs/API-SPEC.md` §서비스 계정
- **영향 범위:** 관리 API `POST /api/v1/service-accounts/{id}/keys` **1개 엔드포인트만**
- **헤드리스 런타임 영향:** **없음**(인증 경로·토큰 형식·401 계약 전부 불변)

---

## 1. 무엇이 바뀌었나

기존에는 스펙 §2B 의 "무기한 또는 **90일 이상**"이 클라이언트 운영 가이드일 뿐 서버가 강제하지 않아,
`expires_at` 이 미래이기만 하면(예: 1초 뒤) 키가 발급됐다. 운영자가 실수로 초단기 키를 발급하면
헤드리스가 조기 401 로 죽는 문제가 있어 **서버가 발급 시점에 하한을 강제**하도록 변경했다.

| 입력 `expires_at` | 변경 전 | 변경 후 |
|---|---|---|
| 생략 / `0` (무기한) | 201 | **201 (동일 — 하한 적용 대상 아님)** |
| 과거·현재 | 400 `expires_at must be in the future` | 동일 |
| 미래, `now + 90d` 이상 | 201 | **201 (동일)** |
| 미래, 90일 미만 | 201 ⚠️ | **400 `expires_at must be at least 90 days in the future`** |

- 경계: 정확히 `now + 90일` 은 **통과**한다(`>=` 비교).
- 기준 시각은 **서버의 발급 시각**(UTC, 초 단위 절삭)이다. 클라이언트 시계와 수 초 오차가 있을 수 있으므로,
  90일 정확히를 노리지 말고 여유를 둘 것(권장: 180일 또는 무기한).
- **이미 발급된 키는 영향받지 않는다.** 하한은 발급 시점에만 적용되며, 인증 경로의 만료 판정 로직은 그대로다.

## 2. 요청/응답 계약 (변경 없음)

```jsonc
// POST /api/v1/service-accounts/{id}/keys   (admin JWT)
// req — expires_at 은 unix epoch seconds(int64), 생략/0 = 무기한
{ "expires_at": 1801036800 }

// 201 — 평문 token 은 이 응답에만 노출된다(서버는 SHA-256 해시만 보관)
{ "key_id": "…", "token": "oma_sa_AbC…", "expires_at": 1801036800 }

// 400 — 90일 하한 위반 (평면 에러 envelope)
{ "code": "BAD_REQUEST", "message": "expires_at must be at least 90 days in the future" }
```

## 3. 클라이언트 조치 사항

1. **키 발급 UI/스크립트를 쓰는 경우에만** 조치가 필요하다. 헤드리스 호스트 런타임 코드는 손댈 것이 없다.
2. 만료일 입력에 클라이언트 측 하한(오늘 + 90일)을 걸고, 그 미만은 서버 왕복 전에 막을 것.
3. 400 응답 메시지 `expires_at must be at least 90 days in the future` 를 사용자에게 그대로 노출하거나
   "만료일은 최소 90일 이후여야 합니다" 로 번역해 표시할 것.
4. `expires_at` 은 RFC3339 가 아니라 **unix epoch seconds `int64`** 다(0 sentinel 표현 때문의 의도적 이탈).
   기존 계약과 동일하지만 이 엔드포인트를 새로 붙이는 경우 주의.

## 4. 권장 운영 값

| 용도 | 권장 `expires_at` |
|---|---|
| 상시 가동 헤드리스 호스트 | **무기한(생략/0)** + `last_used_at` 모니터링 + 필요 시 즉시 폐기 |
| CI 러너 / 임시 워커 | 180일 (분기 회전) |
| 감사 요건상 만료가 필수인 환경 | 90일 (회전 주기를 캘린더에 고정) |

무중단 회전: 새 키 발급 → 호스트 시크릿 교체 → 구 키 `DELETE /api/v1/service-accounts/{id}/keys/{key_id}`.
계정당 다중 키가 동시 유효하므로 다운타임 없이 교체된다.

## 5. 변경 없음을 보증하는 항목

- 토큰 형식 `oma_sa_<random>` · `Authorization: Bearer` 헤더 · 접두사 기반 API키/JWT 분기
- 401 계약(미존재·폐기·만료·계정폐기·인프라 오류 전부 401, 5xx 없음)
- 나머지 관리 엔드포인트 5개(계정 생성/목록/폐기, 키 목록/폐기)의 요청·응답·상태코드
- 에이전트 레지스트리(`/api/v1/agents*`)·A2A 토큰 브로커 계약

---

**서버 측 반영:** `internal/domain/serviceaccount/model.go` 의 `MinKeyLifetime = 90 * 24h` + `IssueKeyCommand.Validate`.
빌드/vet/gofmt clean, 도메인·유스케이스·핸들러·레포 테스트 전부 통과.
