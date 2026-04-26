---
name: go-api-architect
description: "Go API 서버 아키텍처 설계 스킬. 프로젝트 디렉토리 구조, 기술 스택 선택, 패키지 구성, go.mod 의존성, 레포지토리 인터페이스 계약을 설계한다. 'Go 프로젝트 구조 잡아줘', '기술 스택 골라줘', 'Go 아키텍처 설계', 'Go 패키지 구성', 'go.mod 설정' 등 아키텍처/설계 관련 요청 시 반드시 이 스킬을 사용할 것."
---

## 역할

Go API 서버 개발의 청사진을 만든다. 이후 모든 에이전트가 이 설계를 기준으로 작업한다.

## 실행 순서

1. **요구사항 파악** — 개발할 API 기능, 규모, 성능 요구사항 확인
2. **기술 스택 선택** — `references/tech-stack.md` 참조
3. **디렉토리 구조 설계** — `references/project-layout.md` 참조
4. **인터페이스 계약 정의** — 레포지토리 인터페이스를 먼저 정의하여 레이어 간 결합 차단
5. **의존성 목록 작성** — 필요한 go 패키지를 `go get` 명령 형태로 명시
6. **산출물 작성** — `_workspace/01_architect_design.md`에 모든 결정사항 기록

## 산출물 형식

`_workspace/01_architect_design.md`는 다음 섹션을 포함한다:

```markdown
# 아키텍처 설계

## 기술 스택
| 역할 | 선택 | 이유 |
|------|------|------|

## 디렉토리 구조
(트리 형태)

## 레포지토리 인터페이스
(Go 코드 블록)

## 의존성
(go get 명령 목록)

## 에러 처리 전략
(커스텀 에러 타입 정의)

## 가정사항
(불분명한 요구사항에 대한 결정)
```

## 아키텍처 원칙

**레이어 의존 방향:** handler → service → repository → DB
- 각 레이어는 인터페이스로만 하위 레이어에 의존한다
- 도메인 모델은 어떤 레이어도 참조할 수 있다 (단방향 의존 예외)

**패키지 구성:**
- `internal/` — 외부 공개 불필요한 코드 전체
- `pkg/` — 다른 프로젝트에서 재사용 가능한 코드
- `cmd/server/` — 진입점 (main.go, 의존성 조립)

**에러 타입:**
```go
type AppError struct {
    Code    int
    Message string
    Err     error
}

var (
    ErrNotFound     = &AppError{Code: 404, Message: "not found"}
    ErrUnauthorized = &AppError{Code: 401, Message: "unauthorized"}
    ErrConflict     = &AppError{Code: 409, Message: "conflict"}
)
```

세부 기술 스택 선택 기준은 `references/tech-stack.md`, 표준 레이아웃 상세는 `references/project-layout.md` 참조.
