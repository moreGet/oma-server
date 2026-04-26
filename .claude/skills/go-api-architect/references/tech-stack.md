# 기술 스택 선택 가이드

## HTTP 프레임워크

| 프레임워크 | 적합한 경우 | 특징 |
|-----------|------------|------|
| `gin-gonic/gin` | 빠른 개발, 복잡한 API | 미들웨어 생태계 풍부, 검증 내장 |
| `go-chi/chi` | 표준 라이브러리 친화, 미니멀 | `net/http` 호환, 라우터만 제공 |
| `gofiber/fiber` | 고성능, Node.js 개발자 전환 | Fasthttp 기반, 메모리 효율 |
| `labstack/echo` | 미들웨어 커스터마이징 | 간결한 API, 타입 안전 핸들러 |

**기본 선택:** `gin` (생태계·문서화가 가장 성숙)

## 데이터베이스 접근

| 라이브러리 | 적합한 경우 |
|-----------|------------|
| `jmoiron/sqlx` | SQL 직접 제어, 성능 민감 |
| `gorm.io/gorm` | 빠른 CRUD 개발, 복잡한 관계 |
| `sqlc` (코드 생성) | 타입 안전 쿼리, 대규모 팀 |

**기본 선택:** `gorm` (빠른 개발) / 성능 민감 시 `sqlx`

## 설정 관리

```go
// viper 사용 패턴
type Config struct {
    Server   ServerConfig   `mapstructure:"server"`
    Database DatabaseConfig `mapstructure:"database"`
}

type ServerConfig struct {
    Port    int    `mapstructure:"port"`
    Mode    string `mapstructure:"mode"` // debug | release
}

type DatabaseConfig struct {
    DSN      string `mapstructure:"dsn"`
    MaxConns int    `mapstructure:"max_conns"`
}
```

## 로깅

```go
// zap 초기화 패턴
func NewLogger(mode string) *zap.Logger {
    if mode == "production" {
        logger, _ := zap.NewProduction()
        return logger
    }
    logger, _ := zap.NewDevelopment()
    return logger
}
```

## 인증

| 방식 | 라이브러리 | 적합한 경우 |
|------|-----------|------------|
| JWT | `golang-jwt/jwt` | 상태 없는 인증 |
| Session | `gorilla/sessions` | 서버 사이드 세션 |
| OAuth2 | `golang.org/x/oauth2` | 소셜 로그인 |

## 테스팅

```
github.com/stretchr/testify  — assert/mock
github.com/DATA-DOG/go-sqlmock  — DB mock (sqlx 사용 시)
```
