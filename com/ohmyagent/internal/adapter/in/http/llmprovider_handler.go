package httpin

import (
	"errors"
	"net/http"
	"time"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// ProviderHandler 는 /api/v1/llm-providers 그룹의 HTTP 핸들러다.
type ProviderHandler struct {
	svc domainllmprovider.Service
}

// NewProviderHandler 는 ProviderHandler 를 생성한다.
func NewProviderHandler(svc domainllmprovider.Service) *ProviderHandler {
	return &ProviderHandler{svc: svc}
}

// --- GET /api/v1/llm-providers ---

func (h *ProviderHandler) List(w http.ResponseWriter, r *http.Request) error {
	providers, err := h.svc.List(r.Context(), actorID(r))
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, mapSlice(providers, toProviderResp))
	return nil
}

// --- GET /api/v1/llm-providers/{id} ---

func (h *ProviderHandler) Get(w http.ResponseWriter, r *http.Request) error {
	p, err := h.svc.Get(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toProviderResp(p))
	return nil
}

// --- POST /api/v1/llm-providers ---

func (h *ProviderHandler) Create(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[createProviderReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	p, err := h.svc.Create(r.Context(), domainllmprovider.CreateCommand{
		Name:         req.Name,
		ProviderType: domainllmprovider.ProviderType(req.ProviderType),
		IsActive:     req.IsActive,
		Config:       fromConfigDTO(req.Config),
		ActorID:      actorID(r),
	})
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, toProviderResp(p))
	return nil
}

// --- PATCH /api/v1/llm-providers/{id}/config ---

func (h *ProviderHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[updateConfigReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	p, err := h.svc.UpdateConfig(r.Context(), domainllmprovider.UpdateConfigCommand{
		ID:      r.PathValue("id"),
		Config:  fromConfigDTO(req.Config),
		ActorID: actorID(r),
	})
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toProviderResp(p))
	return nil
}

// --- PUT /api/v1/llm-providers/{id}/activate ---

func (h *ProviderHandler) Activate(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Activate(r.Context(), domainllmprovider.ActivateCommand{
		ID:      r.PathValue("id"),
		ActorID: actorID(r),
	}); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, messageResp{Message: "provider activated"})
	return nil
}

// --- DELETE /api/v1/llm-providers/{id} ---

func (h *ProviderHandler) Delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Delete(r.Context(), domainllmprovider.DeleteCommand{
		ID:      r.PathValue("id"),
		ActorID: actorID(r),
	}); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// --- POST /api/v1/llm-providers/{id}/test (연결 테스트) ---

func (h *ProviderHandler) Test(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.TestConnection(r.Context(), actorID(r), r.PathValue("id")); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, messageResp{Message: "connection ok"})
	return nil
}

// DTO (snake_case, gin binding 태그 → 커맨드 Validate 로 대체)

type providerConfigDTO struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// APIKey 는 입력 전용(평문). 서버가 암호화해 저장하며 응답에는 절대 포함하지 않는다.
	APIKey string `json:"api_key,omitempty"`
	// APIKeySet 은 출력 전용: 직접 저장된(암호화된) API 키 존재 여부(마스킹).
	APIKeySet bool `json:"api_key_set"`
	MaxTokens int  `json:"max_tokens,omitempty"`
	// Reasoning 은 추론 강도(reasoning_effort). 서버(관리자)가 정하며 채팅 요청에서는 지정할 수 없다.
	Reasoning string `json:"reasoning,omitempty"`
	// APIStyle 은 OpenAI 호출 방식(chat_completions 기본 | responses).
	APIStyle    string         `json:"api_style,omitempty"`
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

type createProviderReq struct {
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	IsActive     bool              `json:"is_active,omitempty"`
	Config       providerConfigDTO `json:"config"`
}

type updateConfigReq struct {
	Config providerConfigDTO `json:"config"`
}

type providerResp struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	IsActive     bool              `json:"is_active"`
	ProviderType string            `json:"provider_type"`
	Config       providerConfigDTO `json:"config"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	CreatedBy    string            `json:"created_by,omitempty"`
	UpdatedBy    string            `json:"updated_by,omitempty"`
}

type messageResp struct {
	Message string `json:"message"`
}

func toProviderResp(p domainllmprovider.LLMProvider) providerResp {
	return providerResp{
		ID:           p.ID,
		Name:         p.Name,
		IsActive:     p.IsActive,
		ProviderType: string(p.ProviderType),
		Config: providerConfigDTO{
			Endpoint:  p.Config.Endpoint,
			Model:     p.Config.Model,
			APIKeyEnv: p.Config.APIKeyEnv,
			// 직접 저장된 키는 마스킹: 존재 여부만 노출하고 값(암호문/평문)은 절대 반환하지 않는다.
			APIKeySet:   p.Config.APIKey != "",
			MaxTokens:   p.Config.MaxTokens,
			Reasoning:   p.Config.Reasoning,
			APIStyle:    p.Config.APIStyle,
			ExtraParams: p.Config.ExtraParams,
		},
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		CreatedBy: p.CreatedBy,
		UpdatedBy: p.UpdatedBy,
	}
}

func fromConfigDTO(c providerConfigDTO) domainllmprovider.ProviderConfig {
	return domainllmprovider.ProviderConfig{
		Endpoint:    c.Endpoint,
		Model:       c.Model,
		APIKeyEnv:   c.APIKeyEnv,
		APIKey:      c.APIKey, // 평문 입력 → 유스케이스가 암호화
		MaxTokens:   c.MaxTokens,
		Reasoning:   c.Reasoning,
		APIStyle:    c.APIStyle,
		ExtraParams: c.ExtraParams,
	}
}

// providerErrToHTTP 는 llmprovider 도메인 에러를 AppError 로 매핑한다.
func providerErrToHTTP(err error) error {
	var ve *domainllmprovider.ErrValidation
	switch {
	case errors.As(err, &ve):
		return ErrBadRequest(ve.Msg)
	case errors.Is(err, domainllmprovider.ErrNotFound):
		return ErrNotFound("llm provider not found")
	case errors.Is(err, domainllmprovider.ErrNoActiveProvider):
		return ErrNotFound("no active llm provider")
	case errors.Is(err, domainllmprovider.ErrConflict):
		return ErrConflict("provider already exists")
	case errors.Is(err, domainllmprovider.ErrChatUnsupported):
		return ErrBadGateway("provider does not support chat").WithCause(err)
	case errors.Is(err, domainllmprovider.ErrUpstream):
		// 연결 테스트 실패 원인(엔드포인트·벤더 응답)은 서버 로그에만 남긴다.
		return ErrBadGateway("provider connection failed").WithCause(err)
	// accessGate(authUC) 가 반환하는 인가 에러가 새는 경우 매핑.
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("permission denied")
	case errors.Is(err, domainauth.ErrNotFound):
		return ErrForbidden("permission denied")
	default:
		return err
	}
}
