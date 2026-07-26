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
	Env       string          // APP_ENV 에서 주입(yaml 아님)
	resetDB   bool            // APP_DB_RESET 에서 주입(yaml 아님). DB 파괴적 초기화 옵트인.
	Server    ServerConfig    `yaml:"server"`
	Security  SecurityConfig  `yaml:"security"`
	Database  DatabaseConfig  `yaml:"database"`
	Auth      AuthConfig      `yaml:"auth"`
	Messaging MessagingConfig `yaml:"messaging"`
	Registry  RegistryConfig  `yaml:"registry"`
}

// RegistryConfig 는 에이전트 레지스트리(등록·발견·생존성·A2A 토큰 브로커) 설정이다.
// heartbeat_interval/lease_ttl 은 register/heartbeat 응답으로 내려가 클라이언트 루프 주기를 결정한다.
// 미설정(0) 시 유스케이스 권장 기본값(15s/45s/120s)을 쓴다.
type RegistryConfig struct {
	HeartbeatInterval Duration `yaml:"heartbeat_interval"` // 권장 15s
	LeaseTTL          Duration `yaml:"lease_ttl"`          // 권장 45s(= 3×interval)
	TokenTTL          Duration `yaml:"token_ttl"`          // A2A 브로커 토큰 수명(권장 120s)
	SweepInterval     Duration `yaml:"sweep_interval"`     // offline 오래된 레코드 정리 주기. 0 = 비활성
}

// MessagingConfig 는 채팅 실시간 전파 방식 설정이다.
//   - broadcaster: "memory"(기본, 단일 인스턴스) | "redis"(다중 인스턴스 pub/sub)
type MessagingConfig struct {
	Broadcaster string      `yaml:"broadcaster"` // memory | redis
	Redis       RedisConfig `yaml:"redis"`
}

// RedisConfig 는 redis broadcaster 접속 설정이다(broadcaster=redis 일 때).
type RedisConfig struct {
	Addr     string `yaml:"addr"`     // host:port (예: localhost:6379)
	Password string `yaml:"password"` // 운영은 env(APP_MESSAGING_REDIS_PASSWORD) override
	DB       int    `yaml:"db"`       // 논리 DB 번호(기본 0)
	Channel  string `yaml:"channel"`  // pub/sub 채널명(미설정 시 기본값)
}

// UsesRedisBroadcaster 는 redis 전파 방식 사용 여부를 반환한다.
func (c *Config) UsesRedisBroadcaster() bool {
	return strings.EqualFold(strings.TrimSpace(c.Messaging.Broadcaster), "redis")
}

// 참고: 도구 정책(tool_policy)·위험명령 차단(command_policy)·클라이언트 버전(client_version)은
// 이제 DB 에서 관리되고 어드민(`/admin/tools`, `/admin/client`)에서 편집한다. yaml 설정이 아니다.

// ServerConfig 는 HTTP 서버 설정이다.
type ServerConfig struct {
	Port         int      `yaml:"port"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
}

// SecurityConfig 는 CORS 등 보안 관련 설정이다.
type SecurityConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
	// EncryptionSecret 는 Provider API 키 등 시크릿의 AES-GCM 암호화 키 소스다.
	// 코드 기본값 금지: yaml(security.encryption_secret) 또는 env(APP_ENCRYPTION_SECRET)로만 주입.
	EncryptionSecret string `yaml:"encryption_secret"`
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
// 기본 false. APP_DB_RESET=1(true/yes/on) 옵트인 시에만 동작하며 prod 에서는 절대 초기화하지 않는다.
func (c *Config) ResetsDatabase() bool {
	return c.resetDB && !c.isProduction()
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
	cfg.Database.DSN = expandHomePath(cfg.Database.DSN)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}
	return cfg, nil
}

// expandHomePath 는 DSN 선두 `~/`(또는 `file:~/`)를 홈 디렉터리로 확장한다(/mnt/c Windows 마운트의 sqlite I/O 이슈 회피).
// 홈을 알 수 없으면 원본을 그대로 반환한다.
func expandHomePath(dsn string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dsn
	}
	switch {
	case strings.HasPrefix(dsn, "file:~/"):
		return "file:" + home + "/" + strings.TrimPrefix(dsn, "file:~/")
	case strings.HasPrefix(dsn, "~/"):
		return home + "/" + strings.TrimPrefix(dsn, "~/")
	default:
		return dsn
	}
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

// injectSecrets 는 환경변수 비밀 값을 주입한다(YAML 값보다 우선).
// APP_AUTH_JWT_SECRET/APP_DATABASE_DSN/APP_AUTH_SEED_ADMIN_PASSWORD/APP_ENCRYPTION_SECRET 등을 대응 필드로.
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
	if v := strings.TrimSpace(os.Getenv("APP_ENCRYPTION_SECRET")); v != "" {
		c.Security.EncryptionSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("APP_MESSAGING_REDIS_PASSWORD")); v != "" {
		c.Messaging.Redis.Password = v
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_DB_RESET"))) {
	case "1", "true", "yes", "on":
		c.resetDB = true
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
