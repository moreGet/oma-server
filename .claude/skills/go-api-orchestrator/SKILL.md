---
name: go-api-orchestrator
description: "Go 언어 API 서버 개발 전 과정을 지휘하는 마스터 오케스트레이터 스킬. 아키텍처 설계부터 데이터 레이어, HTTP 핸들러, 미들웨어, 테스팅까지 에이전트 팀을 조율하여 완전한 Go API 서버를 구축한다. 'Go API 서버 만들어줘', 'Go 백엔드 구축', 'REST API 개발', 'Gin 서버 만들어줘', 'Go CRUD API', 'Go HTTP 서버', 'Go 서버 개발', '다시 실행', '재실행', '엔드포인트 추가', '기능 추가', '이전 결과 기반으로 수정', '업데이트' 등 Go API 서버 개발 전반 또는 후속 작업 요청 시 반드시 이 스킬을 트리거할 것."
---

## 실행 모드

**에이전트 팀 — 파이프라인 패턴**

architect → data-engineer → implementer → qa-engineer 순서로 파이프라인을 구성한다. 각 에이전트는 이전 에이전트의 산출물을 입력으로 받는다.

모든 Agent 호출 시 `model: "opus"` 파라미터를 명시한다.

---

## Phase 0: 컨텍스트 확인

워크플로우 시작 시 가장 먼저 `_workspace/` 디렉토리를 확인한다.

- `_workspace/` 미존재 → **초기 실행** (Phase 1부터 전체 실행)
- `_workspace/` 존재 + 사용자가 부분 수정 요청 → **부분 재실행** (해당 Phase만 재호출)
- `_workspace/` 존재 + 새 기능 추가 요청 → **기능 추가 실행** (architect 생략, 이후 단계만 실행)

---

## Phase 1: 요구사항 수집 및 architect 실행

사용자 요청에서 다음을 파악한다:
- 개발할 API 기능 (엔드포인트, 도메인 엔티티)
- 선호 기술 스택 (명시 없으면 gin + gorm + PostgreSQL 기본값)
- 인증 필요 여부, DB 종류

**architect 에이전트 실행:**

```
Agent({
  subagent_type: "general-purpose",
  model: "opus",
  prompt: "[architect.md 내용 기반] 요구사항: {사용자 요청 내용}. _workspace/01_architect_design.md에 설계를 작성하라."
})
```

---

## Phase 2: data-engineer 실행

architect 산출물을 기반으로 데이터 레이어를 구현한다.

**data-engineer 에이전트 실행:**

```
Agent({
  subagent_type: "general-purpose",
  model: "opus",
  prompt: "[data-engineer.md 내용 기반] _workspace/01_architect_design.md를 읽고, 도메인 모델·레포지토리·마이그레이션을 구현하라. 완료 후 _workspace/02_data_summary.md를 작성하라."
})
```

---

## Phase 3: implementer 실행

데이터 레이어 완성 후 HTTP 레이어를 구현한다.

**implementer 에이전트 실행:**

```
Agent({
  subagent_type: "general-purpose",
  model: "opus",
  prompt: "[implementer.md 내용 기반] _workspace/01_architect_design.md, _workspace/02_data_summary.md를 읽고, 핸들러·서비스·미들웨어·라우터·main.go를 구현하라. 완료 후 _workspace/03_implementer_summary.md를 작성하라."
})
```

---

## Phase 4: qa-engineer 실행

구현 완료 후 테스트를 작성하고 경계면을 검증한다.

**qa-engineer 에이전트 실행:**

```
Agent({
  subagent_type: "general-purpose",
  model: "opus",
  prompt: "[qa-engineer.md 내용 기반] _workspace/03_implementer_summary.md와 internal/ 하위 코드를 읽고, 핸들러·서비스 테스트를 작성하라. 경계면 불일치 발견 시 _workspace/04_qa_report.md에 기록하라."
})
```

---

## Phase 5: 결과 통합 및 보고

모든 에이전트 완료 후:
1. 생성된 파일 목록 확인 (`find . -name "*.go" | grep -v vendor`)
2. `go build ./...` 실행 가능 여부 확인
3. `_workspace/04_qa_report.md` 이슈 확인
4. 사용자에게 최종 보고:
   - 구현된 엔드포인트 목록
   - 실행 방법
   - 발견된 이슈 (있을 경우)

---

## 데이터 전달 프로토콜

| Phase | 입력 | 출력 파일 |
|-------|------|----------|
| architect | 사용자 요구사항 | `_workspace/01_architect_design.md` |
| data-engineer | `01_architect_design.md` | `internal/model/`, `internal/repository/`, `migrations/`, `_workspace/02_data_summary.md` |
| implementer | `01`, `02` | `internal/handler/`, `internal/service/`, `internal/middleware/`, `internal/router/`, `cmd/`, `_workspace/03_implementer_summary.md` |
| qa-engineer | `03` + 실제 코드 | `internal/**/*_test.go`, `_workspace/04_qa_report.md` |

---

## 에러 핸들링

- 에이전트 실패 시 1회 재시도, 재실패 시 해당 Phase 없이 다음 단계 진행 + 보고서에 명시
- qa-engineer에서 치명적 버그 발견 시 implementer 재실행

---

## 테스트 시나리오

**정상 흐름:**
"User CRUD API 만들어줘. Gin 프레임워크 쓰고 PostgreSQL 연결해줘."
→ architect가 gin+gorm+postgres 스택으로 설계 → data-engineer가 User 모델·레포지토리 구현 → implementer가 5개 엔드포인트 구현 → qa-engineer가 핸들러·서비스 테스트 작성

**에러 흐름:**
data-engineer가 잘못된 인터페이스를 구현한 경우 → implementer가 컴파일 오류 발견 → data-engineer 재실행

**후속 작업:**
"JWT 인증 추가해줘"
→ Phase 0: `_workspace/` 존재 확인 → architect 생략, implementer에게 JWT 미들웨어 추가 지시 → qa-engineer에게 인증 테스트 추가 지시
