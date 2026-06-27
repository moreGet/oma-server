package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpandHomePath(t *testing.T) {
	t.Setenv("HOME", "/home/test")

	assert.Equal(t, "file:/home/test/aiagent.db", expandHomePath("file:~/aiagent.db"))
	assert.Equal(t, "/home/test/data/x.db", expandHomePath("~/data/x.db"))
	// 절대/일반 경로·다른 드라이버 DSN 은 그대로 둔다.
	assert.Equal(t, "file:/var/lib/x.db", expandHomePath("file:/var/lib/x.db"))
	assert.Equal(t, "user:pw@tcp(localhost:3306)/db", expandHomePath("user:pw@tcp(localhost:3306)/db"))
}

func TestInjectSecrets_EnvOverridesYAML(t *testing.T) {
	t.Setenv("APP_AUTH_JWT_SECRET", "env-jwt")
	t.Setenv("APP_DATABASE_DSN", "env-dsn")
	t.Setenv("APP_AUTH_SEED_ADMIN_PASSWORD", "env-pw")
	t.Setenv("APP_ENCRYPTION_SECRET", "env-enc")
	t.Setenv("APP_DB_RESET", "yes")

	c := &Config{}
	c.Auth.JWTSecret = "yaml-jwt"
	c.Database.DSN = "yaml-dsn"
	c.injectSecrets()

	assert.Equal(t, "env-jwt", c.Auth.JWTSecret) // env 가 yaml 을 덮어쓴다
	assert.Equal(t, "env-dsn", c.Database.DSN)
	assert.Equal(t, "env-pw", c.Auth.SeedAdminPassword)
	assert.Equal(t, "env-enc", c.Security.EncryptionSecret)
	assert.True(t, c.resetDB) // APP_DB_RESET=yes → 초기화 옵트인
}

func TestInjectSecrets_NoEnvKeepsYAML(t *testing.T) {
	t.Setenv("APP_AUTH_JWT_SECRET", "")
	t.Setenv("APP_DATABASE_DSN", "")
	t.Setenv("APP_AUTH_SEED_ADMIN_PASSWORD", "")
	t.Setenv("APP_ENCRYPTION_SECRET", "")
	t.Setenv("APP_DB_RESET", "")

	c := &Config{}
	c.Auth.JWTSecret = "yaml-jwt"
	c.injectSecrets()

	assert.Equal(t, "yaml-jwt", c.Auth.JWTSecret) // env 없으면 yaml 유지
	assert.False(t, c.resetDB)
}
