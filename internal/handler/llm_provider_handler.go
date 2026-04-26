package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"OhMyAgent.AiAgent.Server/internal/domain"
	"OhMyAgent.AiAgent.Server/internal/dto"
)

// LLMProviderServicePort 는 핸들러가 의존하는 서비스 표면이다.
// 테스트 가능성을 위해 concrete 타입이 아닌 인터페이스에 의존한다.
// 실제 구현체는 application.LLMProviderService 이며 모든 메서드가 만족된다.
type LLMProviderServicePort interface {
	ListProviders(ctx context.Context) ([]*domain.LLMProvider, error)
	GetProvider(ctx context.Context, id int64) (*domain.LLMProvider, error)
	CreateProvider(ctx context.Context, provider *domain.LLMProvider) (*domain.LLMProvider, error)
	ActivateProvider(ctx context.Context, id int64) error
	UpdateProviderConfig(ctx context.Context, id int64, config domain.ProviderConfig) error
	DeleteProvider(ctx context.Context, id int64) error
}

// LLMProviderHandler 는 /api/v1/admin/llm-providers 그룹의 HTTP 핸들러이다.
// 핸들러는 얇게: I/O 바인딩, 매핑, 에러 변환만 담당하고 비즈니스 로직은 서비스에 위임한다.
type LLMProviderHandler struct {
	svc LLMProviderServicePort
}

// NewLLMProviderHandler 는 핸들러를 생성한다.
func NewLLMProviderHandler(svc LLMProviderServicePort) *LLMProviderHandler {
	return &LLMProviderHandler{svc: svc}
}

// ---------------------------------------------------------------------------
// GET /api/v1/admin/llm-providers
// ---------------------------------------------------------------------------

// ListProviders 는 전체 Provider 목록을 반환한다.
func (h *LLMProviderHandler) ListProviders(c *gin.Context) {
	providers, err := h.svc.ListProviders(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	out := make([]dto.ProviderResponse, 0, len(providers))
	for _, p := range providers {
		out = append(out, toProviderResponse(p))
	}
	c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// GET /api/v1/admin/llm-providers/:id
// ---------------------------------------------------------------------------

// GetProvider 는 단일 Provider 를 조회한다.
func (h *LLMProviderHandler) GetProvider(c *gin.Context) {
	id, err := parseIDParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
		return
	}
	provider, err := h.svc.GetProvider(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProviderResponse(provider))
}

// ---------------------------------------------------------------------------
// POST /api/v1/admin/llm-providers
// ---------------------------------------------------------------------------

// CreateProvider 는 새 Provider 를 등록한다.
func (h *LLMProviderHandler) CreateProvider(c *gin.Context) {
	var req dto.CreateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{
			Error:   "invalid request body",
			Details: err.Error(),
		})
		return
	}
	created, err := h.svc.CreateProvider(c.Request.Context(), fromCreateRequest(&req))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toProviderResponse(created))
}

// ---------------------------------------------------------------------------
// PUT /api/v1/admin/llm-providers/:id/activate
// ---------------------------------------------------------------------------

// ActivateProvider 는 지정 ID 를 활성화 + 캐시 무효화한다.
func (h *LLMProviderHandler) ActivateProvider(c *gin.Context) {
	id, err := parseIDParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
		return
	}
	if err := h.svc.ActivateProvider(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.MessageResponse{Message: "provider activated"})
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/admin/llm-providers/:id/config
// ---------------------------------------------------------------------------

// UpdateProviderConfig 는 config_json 만 갱신한다.
func (h *LLMProviderHandler) UpdateProviderConfig(c *gin.Context) {
	id, err := parseIDParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
		return
	}
	var req dto.UpdateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{
			Error:   "invalid request body",
			Details: err.Error(),
		})
		return
	}
	if err := h.svc.UpdateProviderConfig(c.Request.Context(), id, fromConfigDTO(req.Config)); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.MessageResponse{Message: "config updated"})
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/admin/llm-providers/:id
// ---------------------------------------------------------------------------

// DeleteProvider 는 Provider 를 삭제한다.
func (h *LLMProviderHandler) DeleteProvider(c *gin.Context) {
	id, err := parseIDParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
		return
	}
	if err := h.svc.DeleteProvider(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 매핑 헬퍼 (DTO ↔ domain)
// ---------------------------------------------------------------------------

func toProviderResponse(p *domain.LLMProvider) dto.ProviderResponse {
	return dto.ProviderResponse{
		ID:           p.ID,
		Name:         p.Name,
		IsActive:     p.IsActive,
		ProviderType: string(p.ProviderType),
		Config: dto.ProviderConfigDTO{
			Endpoint:    p.Config.Endpoint,
			Model:       p.Config.Model,
			APIKeyEnv:   p.Config.APIKeyEnv,
			MaxTokens:   p.Config.MaxTokens,
			ExtraParams: p.Config.ExtraParams,
		},
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func fromCreateRequest(req *dto.CreateProviderRequest) *domain.LLMProvider {
	return &domain.LLMProvider{
		Name:         req.Name,
		IsActive:     req.IsActive,
		ProviderType: domain.ProviderType(req.ProviderType),
		Config:       fromConfigDTO(req.Config),
	}
}

func fromConfigDTO(c dto.ProviderConfigDTO) domain.ProviderConfig {
	return domain.ProviderConfig{
		Endpoint:    c.Endpoint,
		Model:       c.Model,
		APIKeyEnv:   c.APIKeyEnv,
		MaxTokens:   c.MaxTokens,
		ExtraParams: c.ExtraParams,
	}
}

// ---------------------------------------------------------------------------
// 공통 헬퍼: ID 파싱 + 도메인 에러 → HTTP 매핑
// ---------------------------------------------------------------------------

// parseIDParam 은 path 의 :id 를 int64 로 파싱한다.
func parseIDParam(c *gin.Context) (int64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("invalid id path param")
	}
	if id <= 0 {
		return 0, errors.New("id must be positive")
	}
	return id, nil
}

// respondError 는 도메인 에러를 HTTP 상태/메시지로 매핑하여 응답한다.
//
// 매핑:
//   - ErrProviderNotFound | ErrNoActiveProvider → 404
//   - ErrInvalidProvider  | ErrInvalidProviderType → 400
//   - ErrProviderConflict → 409
//   - 그 외 → 500
func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrProviderNotFound),
		errors.Is(err, domain.ErrNoActiveProvider):
		c.JSON(http.StatusNotFound, dto.ErrorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidProvider),
		errors.Is(err, domain.ErrInvalidProviderType):
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrProviderConflict):
		c.JSON(http.StatusConflict, dto.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, dto.ErrorResponse{Error: "internal server error", Details: err.Error()})
	}
}
