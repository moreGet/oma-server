package domain

import (
	"time"
)

// ProviderType 은 LLM 제공자의 배포 유형을 나타낸다.
type ProviderType string

const (
	ProviderTypeLocal    ProviderType = "LOCAL"
	ProviderTypeExternal ProviderType = "EXTERNAL"
)

// ProviderConfig 는 config_json 컬럼에 저장되는 가변 설정의 도메인 표현이다.
// 시크릿(API 키)은 직접 저장하지 않고, APIKeyEnv 에 환경변수 이름만 저장한다.
type ProviderConfig struct {
	Endpoint    string         `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	APIKeyEnv   string         `json:"api_key_env,omitempty"` // 환경변수명만 저장 (보안)
	MaxTokens   int            `json:"max_tokens,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

// LLMProvider 는 도메인 애그리거트 루트.
// sqlc 가 생성한 LlmProvider 와는 분리되어 있으며, 매핑은 어댑터 레이어에서만 수행한다.
type LLMProvider struct {
	ID           int64
	Name         string
	IsActive     bool
	ProviderType ProviderType
	Config       ProviderConfig
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate 는 도메인 엔티티의 최소 유효성을 검증한다.
func (p *LLMProvider) Validate() error {
	if p.Name == "" {
		return ErrInvalidProvider
	}
	if p.ProviderType != ProviderTypeLocal && p.ProviderType != ProviderTypeExternal {
		return ErrInvalidProviderType
	}
	return nil
}
