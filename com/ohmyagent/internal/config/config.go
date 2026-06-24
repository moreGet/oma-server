package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 은 "10s" / "24h" 같은 문자열을 time.Duration 으로 파싱하는 커스텀 타입이다.
// YAML 언마샬 시 문자열을 받아 time.ParseDuration 으로 변환한다.
type Duration time.Duration

// UnmarshalYAML 은 문자열("10s") 또는 숫자(나노초)를 모두 허용한다.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"10s\": %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std 는 time.Duration 값을 반환한다.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config 는 애플리케이션 전역 설정이다.
type Config struct {
	Env      string         // APP_ENV 에서 주입(yaml 아님)
	Server   ServerConfig   `yaml:"server"`
	Security SecurityConfig `yaml:"security"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
}

// ServerConfig 는 HTTP 서버 설정이다.
type ServerConfig struct {
	Port         int      `yaml:"port"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
}

// SecurityConfig 는 CORS 등 보안 관련 설정이다.
type SecurityConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// DatabaseConfig 는 DB 연결 설정이다.
type DatabaseConfig struct {
	Driver       string `yaml:"driver"` // mysql | sqlite
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
}

// AuthConfig 는 JWT / 초기 관리자 시딩 설정이다.
type AuthConfig struct {
	JWTSecret         string   `yaml:"jwt_secret"`
	JWTExpiry         Duration `yaml:"jwt_expiry"`
	SeedAdminUsername string   `yaml:"seed_admin_username"`
	SeedAdminPassword string   `yaml:"seed_admin_password"`
}

// ServerAddr 는 ":8080" 형식의 리스닝 주소를 반환한다.
func (c *Config) ServerAddr() string {
	return fmt.Sprintf(":%d", c.Server.Port)
}

// SeedsInitialAdmin 은 비운영 환경(local/dev/docker)에서만 초기 관리자 시딩을 수행할지 판정한다.
func (c *Config) SeedsInitialAdmin() bool {
	switch c.Env {
	case "local", "dev", "docker":
		return true
	default:
		return false
	}
}

// ResetsDatabase 는 기동 시 DB 를 파괴적으로 초기화(drop & create)할지 판정한다.
// 로컬 개발 편의(매 기동마다 깨끗한 스키마 + 시드 재생성)를 위해 local 에서만 true.
// dev/docker/prod 는 데이터를 보존한다.
func (c *Config) ResetsDatabase() bool {
	return c.Env == "local"
}

// isProduction 은 운영 환경 여부를 반환한다(jwt_secret 필수 검증용).
func (c *Config) isProduction() bool {
	return c.Env == "prod"
}

// Load 는 APP_ENV 에 따라 configs/{env}.yaml 을 읽고, env 비밀을 주입한 뒤 검증한다.
// APP_ENV 미설정 시 "local" 을 사용한다.
func Load() (*Config, error) {
	env := strings.TrimSpace(os.Getenv("APP_ENV"))
	if env == "" {
		env = "local"
	}

	path := fmt.Sprintf("configs/%s.yaml", env)
	cfg, err := loadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: load %s: %w", path, err)
	}
	cfg.Env = env

	cfg.injectSecrets()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}
	return cfg, nil
}

// loadFile 은 YAML 파일을 디코드한다.
func loadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// injectSecrets 는 환경변수로 전달된 비밀 값을 주입한다(YAML 값보다 우선).
//
//	APP_AUTH_JWT_SECRET         → Auth.JWTSecret
//	APP_DATABASE_DSN            → Database.DSN
//	APP_AUTH_SEED_ADMIN_PASSWORD → Auth.SeedAdminPassword
func (c *Config) injectSecrets() {
	if v := strings.TrimSpace(os.Getenv("APP_AUTH_JWT_SECRET")); v != "" {
		c.Auth.JWTSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_DATABASE_DSN")); v != "" {
		c.Database.DSN = v
	}
	if v := os.Getenv("APP_AUTH_SEED_ADMIN_PASSWORD"); v != "" {
		c.Auth.SeedAdminPassword = v
	}
}

// validate 는 필수 필드와 제약을 검증한다.
func (c *Config) validate() error {
	if c.Server.Port <= 0 {
		return fmt.Errorf("server.port must be > 0, got %d", c.Server.Port)
	}
	switch c.Database.Driver {
	case "mysql", "sqlite":
	default:
		return fmt.Errorf("database.driver must be one of {mysql, sqlite}, got %q", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if c.isProduction() && c.Auth.JWTSecret == "" {
		return fmt.Errorf("auth.jwt_secret is required in production")
	}
	return nil
}
