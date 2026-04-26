---
name: go-api-test
description: "Go API 서버 테스트 작성 스킬. 핸들러 단위 테스트, 서비스 단위 테스트, 통합 테스트를 작성한다. 'Go 테스트 만들어줘', '핸들러 테스트', '서비스 테스트', 'mock 테스트', 'testify 사용', 'httptest', 'table-driven test', 'Go 단위 테스트', 'API 테스트' 등 테스팅 관련 요청 시 반드시 이 스킬을 사용할 것."
---

## 역할

Go API 서버의 테스트 코드를 작성하고 경계면 정합성을 검증한다.

## 실행 순서

1. **구현 코드 읽기** — `_workspace/03_implementer_summary.md`, `internal/handler/`, `internal/service/`, `internal/dto/` 확인
2. **경계면 검증** — 핸들러 응답 shape과 서비스 반환 타입 교차 비교
3. **서비스 테스트 작성** — `internal/service/*_test.go`
4. **핸들러 테스트 작성** — `internal/handler/*_test.go`
5. **QA 보고서 작성** — `_workspace/04_qa_report.md`

## 핸들러 테스트 패턴

```go
// internal/handler/user_handler_test.go
package handler_test

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/gin-gonic/gin"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"
    "OhMyAgent.AiAgent.Server/internal/dto"
    "OhMyAgent.AiAgent.Server/internal/handler"
)

// Mock 서비스
type MockUserService struct {
    mock.Mock
}

func (m *MockUserService) GetUser(ctx context.Context, id int64) (*dto.UserResponse, error) {
    args := m.Called(ctx, id)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*dto.UserResponse), args.Error(1)
}

func TestGetUser(t *testing.T) {
    gin.SetMode(gin.TestMode)

    tests := []struct {
        name       string
        id         string
        mockReturn *dto.UserResponse
        mockError  error
        wantStatus int
    }{
        {
            name:       "success",
            id:         "1",
            mockReturn: &dto.UserResponse{ID: 1, Name: "Alice"},
            wantStatus: http.StatusOK,
        },
        {
            name:       "not found",
            id:         "999",
            mockError:  apperror.ErrNotFound,
            wantStatus: http.StatusNotFound,
        },
        {
            name:       "invalid id",
            id:         "abc",
            wantStatus: http.StatusBadRequest,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            mockSvc := new(MockUserService)
            if tt.mockReturn != nil || tt.mockError != nil {
                mockSvc.On("GetUser", mock.Anything, mock.Anything).
                    Return(tt.mockReturn, tt.mockError)
            }

            h := handler.NewUserHandler(mockSvc)
            r := gin.New()
            r.GET("/users/:id", h.GetUser)

            w := httptest.NewRecorder()
            req := httptest.NewRequest(http.MethodGet, "/users/"+tt.id, nil)
            r.ServeHTTP(w, req)

            assert.Equal(t, tt.wantStatus, w.Code)
        })
    }
}
```

## 서비스 테스트 패턴

```go
// internal/service/user_service_test.go
package service_test

import (
    "context"
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"
    "OhMyAgent.AiAgent.Server/internal/model"
    "OhMyAgent.AiAgent.Server/internal/service"
    "OhMyAgent.AiAgent.Server/pkg/apperror"
)

type MockUserRepository struct {
    mock.Mock
}

func (m *MockUserRepository) FindByID(ctx context.Context, id int64) (*model.User, error) {
    args := m.Called(ctx, id)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*model.User), args.Error(1)
}

func TestGetUser_NotFound(t *testing.T) {
    mockRepo := new(MockUserRepository)
    mockRepo.On("FindByID", mock.Anything, int64(999)).Return(nil, apperror.ErrNotFound)

    svc := service.NewUserService(mockRepo)
    _, err := svc.GetUser(context.Background(), 999)

    assert.ErrorIs(t, err, apperror.ErrNotFound)
    mockRepo.AssertExpectations(t)
}
```

## 경계면 검증 체크리스트

핸들러 테스트 작성 전 반드시 확인:
- [ ] 핸들러가 파싱하는 JSON 필드명 == DTO의 `json` 태그
- [ ] 핸들러가 반환하는 상태 코드 == 설계 문서의 상태 코드 표
- [ ] 서비스가 반환하는 에러 타입 == 핸들러의 에러 분기 조건
- [ ] 응답 DTO의 필드 == 실제 응답 JSON 구조

## 실행 명령

```bash
# 전체 테스트
go test ./...

# 커버리지 확인
go test ./... -cover -coverprofile=coverage.out
go tool cover -html=coverage.out

# 특정 패키지
go test ./internal/handler/... -v -run TestGetUser
```

통합 테스트 패턴은 `references/integration-test.md` 참조.
