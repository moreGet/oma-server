module aiagent

go 1.25

require (
	github.com/go-sql-driver/mysql v1.8.1
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/pressly/goose/v3 v3.21.1
	github.com/rs/cors v1.11.1
	golang.org/x/crypto v0.24.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.30.1
)

// 테스트용(테스트 변경은 사용자 승인 후)
require github.com/stretchr/testify v1.9.0
