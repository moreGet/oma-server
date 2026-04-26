package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	LLM      LLMConfig      `yaml:"llm"`
}

type ServerConfig struct {
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"`
}

type DatabaseConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int    `yaml:"max_conns"`
}

type LLMConfig struct {
	AnthropicAPIKey string `yaml:"anthropic_api_key"`
	OpenAIAPIKey    string `yaml:"openai_api_key"`
}

func (c *Config) ServerAddr() string {
	return fmt.Sprintf(":%d", c.Server.Port)
}

// Load 는 config/config.yaml 을 읽은 후, config/config.local.yaml 이 존재하면 값을 덮어씁니다.
func Load() (*Config, error) {
	cfg, err := loadFile("config/config.yaml")
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if local, err := loadFile("config/config.local.yaml"); err == nil {
		merge(cfg, local)
	}

	return cfg, nil
}

func loadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// merge 는 override 의 비어있지 않은 값으로 base 를 덮어씁니다.
func merge(base, override *Config) {
	if override.Server.Port != 0 {
		base.Server.Port = override.Server.Port
	}
	if override.Server.Mode != "" {
		base.Server.Mode = override.Server.Mode
	}
	if override.Database.DSN != "" {
		base.Database.DSN = override.Database.DSN
	}
	if override.Database.MaxConns != 0 {
		base.Database.MaxConns = override.Database.MaxConns
	}
	if override.LLM.AnthropicAPIKey != "" {
		base.LLM.AnthropicAPIKey = override.LLM.AnthropicAPIKey
	}
	if override.LLM.OpenAIAPIKey != "" {
		base.LLM.OpenAIAPIKey = override.LLM.OpenAIAPIKey
	}
}
