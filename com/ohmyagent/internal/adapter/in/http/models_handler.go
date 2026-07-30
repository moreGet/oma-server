package httpin

import (
	"net/http"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// ModelsHandler 는 GET /api/v1/models (사용 가능한 모델 목록) 핸들러다.
// 등록된 LLM Provider 들로부터 모델 목록을 구성한다.
type ModelsHandler struct {
	svc domainllmprovider.Service
}

// NewModelsHandler 는 ModelsHandler 를 생성한다.
func NewModelsHandler(svc domainllmprovider.Service) *ModelsHandler {
	return &ModelsHandler{svc: svc}
}

type modelDTO struct {
	ID           string `json:"id"`   // /agent/chat 의 model 필드로 전달할 식별자
	Name         string `json:"name"` // Provider 표시명
	ProviderType string `json:"provider_type"`
	Active       bool   `json:"active"`
}

type modelsResp struct {
	Models []modelDTO `json:"models"`
}

// List 는 모델 목록을 반환한다(MinRole user).
func (h *ModelsHandler) List(w http.ResponseWriter, r *http.Request) error {
	providers, err := h.svc.List(r.Context(), actorID(r))
	if err != nil {
		return providerErrToHTTP(err)
	}
	models := make([]modelDTO, 0, len(providers))
	for _, p := range providers {
		id := p.Config.Model
		if id == "" {
			id = p.Name
		}
		models = append(models, modelDTO{
			ID:           id,
			Name:         p.Name,
			ProviderType: string(p.ProviderType),
			Active:       p.IsActive,
		})
	}
	writeJSON(w, http.StatusOK, modelsResp{Models: models})
	return nil
}
