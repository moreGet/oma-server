package httpin

import "net/http"

// SuggestionHandler 는 GET /api/v1/agent/suggestions (동작 힌트 카드, 요구 G) 핸들러다.
// 현재는 stub: 빈 목록을 반환한다(클라이언트는 빈 목록이면 UI 를 자동 숨김).
type SuggestionHandler struct{}

// NewSuggestionHandler 는 SuggestionHandler 를 생성한다.
func NewSuggestionHandler() *SuggestionHandler { return &SuggestionHandler{} }

type suggestionDTO struct {
	Text   string `json:"text"`
	Prompt string `json:"prompt,omitempty"`
	Icon   string `json:"icon,omitempty"`
}

type suggestionsResp struct {
	Suggestions []suggestionDTO `json:"suggestions"`
}

// List 는 제안 목록을 반환한다(현재 빈 목록 stub). workspace_root 쿼리는 향후 활용 예정.
func (h *SuggestionHandler) List(w http.ResponseWriter, r *http.Request) error {
	_ = r.URL.Query().Get("workspace_root") // 향후 컨텍스트 기반 제안에 사용
	writeJSON(w, http.StatusOK, suggestionsResp{Suggestions: []suggestionDTO{}})
	return nil
}
