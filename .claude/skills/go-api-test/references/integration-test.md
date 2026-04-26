# 통합 테스트 패턴

## 테스트용 DB 설정 (TestMain)

```go
// internal/repository/repository_test.go
package repository_test

import (
    "os"
    "testing"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
    "OhMyAgent.AiAgent.Server/internal/model"
)

var testDB *gorm.DB

func TestMain(m *testing.M) {
    var err error
    testDB, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        panic(err)
    }
    testDB.AutoMigrate(&model.User{})

    code := m.Run()
    os.Exit(code)
}
```

## 테스트 픽스처 헬퍼

```go
func createTestUser(t *testing.T, db *gorm.DB) *model.User {
    t.Helper()
    user := &model.User{
        Email:    "test@example.com",
        Password: "hashed_password",
        Name:     "Test User",
    }
    if err := db.Create(user).Error; err != nil {
        t.Fatalf("failed to create test user: %v", err)
    }
    t.Cleanup(func() {
        db.Unscoped().Delete(user)
    })
    return user
}
```

## httptest 서버 통합 테스트

```go
func TestCreateUserIntegration(t *testing.T) {
    // 실제 레포지토리 + 실제 서비스 + 실제 핸들러
    repo := repository.NewUserRepository(testDB)
    svc := service.NewUserService(repo)
    h := handler.NewUserHandler(svc)

    r := gin.New()
    r.POST("/users", h.CreateUser)
    ts := httptest.NewServer(r)
    defer ts.Close()

    body := `{"email":"new@example.com","password":"password123","name":"New User"}`
    resp, err := http.Post(ts.URL+"/users", "application/json", strings.NewReader(body))
    assert.NoError(t, err)
    assert.Equal(t, http.StatusCreated, resp.StatusCode)
}
```
