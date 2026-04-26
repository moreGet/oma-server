package dto

import "time"

// CreateProviderRequest 는 POST /llm-providers 의 요청 바디.
type CreateProviderRequest struct {
	Name         string            `json:"name"          binding:"required"`
	ProviderType string            `json:"provider_type" binding:"required,oneof=LOCAL EXTERNAL"`
	IsActive     bool              `json:"is_active,omitempty"`
	Config       ProviderConfigDTO `json:"config"        binding:"required"`
}

// ProviderConfigDTO 는 도메인 ProviderConfig 의 1:1 표현.
type ProviderConfigDTO struct {
	Endpoint    string         `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	APIKeyEnv   string         `json:"api_key_env,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

// UpdateConfigRequest 는 PATCH /llm-providers/:id/config 의 요청 바디.
type UpdateConfigRequest struct {
	Config ProviderConfigDTO `json:"config" binding:"required"`
}

// ProviderResponse 는 단건/목록 응답 공통 표현.
type ProviderResponse struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	IsActive     bool              `json:"is_active"`
	ProviderType string            `json:"provider_type"`
	Config       ProviderConfigDTO `json:"config"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// ActivateProviderRequest 는 활성화 엔드포인트의 (현재 비어있는) 요청 바디.
type ActivateProviderRequest struct {
	// path param 만 사용. 향후 옵션 (force 등) 추가 가능.
}

// ErrorResponse 는 모든 에러 응답의 공통 포맷.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// MessageResponse 는 단순 성공 메시지 응답.
type MessageResponse struct {
	Message string `json:"message"`
}
