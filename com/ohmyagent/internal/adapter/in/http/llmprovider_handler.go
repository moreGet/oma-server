package httpin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
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
	claims, _ := security.ClaimsFrom(r.Context())
	providers, err := h.svc.List(r.Context(), claims.MemberID)
	if err != nil {
		return providerErrToHTTP(err)
	}
	items := make([]providerResp, 0, len(providers))
	for _, p := range providers {
		items = append(items, toProviderResp(p))
	}
	writeJSON(w, http.StatusOK, items)
	return nil
}

// --- GET /api/v1/llm-providers/{id} ---

func (h *ProviderHandler) Get(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	p, err := h.svc.Get(r.Context(), claims.MemberID, r.PathValue("id"))
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toProviderResp(p))
	return nil
}

// --- POST /api/v1/llm-providers ---

func (h *ProviderHandler) Create(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req createProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	p, err := h.svc.Create(r.Context(), domainllmprovider.CreateCommand{
		Name:         req.Name,
		ProviderType: domainllmprovider.ProviderType(req.ProviderType),
		IsActive:     req.IsActive,
		Config:       fromConfigDTO(req.Config),
		ActorID:      claims.MemberID,
	})
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusCreated, toProviderResp(p))
	return nil
}

// --- PATCH /api/v1/llm-providers/{id}/config ---

func (h *ProviderHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	claims, _ := security.ClaimsFrom(r.Context())
	var req updateConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	p, err := h.svc.UpdateConfig(r.Context(), domainllmprovider.UpdateConfigCommand{
		ID:      r.PathValue("id"),
		Config:  fromConfigDTO(req.Config),
		ActorID: claims.MemberID,
	})
	if err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, toProviderResp(p))
	return nil
}

// --- PUT /api/v1/llm-providers/{id}/activate ---

func (h *ProviderHandler) Activate(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	if err := h.svc.Activate(r.Context(), domainllmprovider.ActivateCommand{
		ID:      r.PathValue("id"),
		ActorID: claims.MemberID,
	}); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, messageResp{Message: "provider activated"})
	return nil
}

// --- DELETE /api/v1/llm-providers/{id} ---

func (h *ProviderHandler) Delete(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	if err := h.svc.Delete(r.Context(), domainllmprovider.DeleteCommand{
		ID:      r.PathValue("id"),
		ActorID: claims.MemberID,
	}); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusNoContent, nil)
	return nil
}

// --- POST /api/v1/llm-providers/{id}/test (연결 테스트) ---

func (h *ProviderHandler) Test(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	if err := h.svc.TestConnection(r.Context(), claims.MemberID, r.PathValue("id")); err != nil {
		return providerErrToHTTP(err)
	}
	writeJSON(w, http.StatusOK, messageResp{Message: "connection ok"})
	return nil
}

// ---------------------------------------------------------------------------
// DTO (snake_case, gin binding 태그 → 커맨드 Validate 로 대체)
// ---------------------------------------------------------------------------

type providerConfigDTO struct {
	Endpoint    string         `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	APIKeyEnv   string         `json:"api_key_env,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
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
			Endpoint:    p.Config.Endpoint,
			Model:       p.Config.Model,
			APIKeyEnv:   p.Config.APIKeyEnv,
			MaxTokens:   p.Config.MaxTokens,
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
		MaxTokens:   c.MaxTokens,
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
		return ErrBadGateway("provider does not support chat")
	case errors.Is(err, domainllmprovider.ErrUpstream):
		return ErrBadGateway("provider connection failed")
	// accessGate(authUC) 가 반환하는 인가 에러가 새는 경우 매핑.
	case errors.Is(err, domainauth.ErrPermission):
		return ErrForbidden("permission denied")
	case errors.Is(err, domainauth.ErrNotFound):
		return ErrForbidden("permission denied")
	default:
		return err
	}
}
