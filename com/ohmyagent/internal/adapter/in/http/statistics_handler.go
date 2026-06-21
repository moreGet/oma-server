package httpin

import (
	"net/http"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// StatsHandler 는 GET /api/v1/statistics (admin 대시보드 집계) 핸들러다.
// 기존 use case 들을 조합해 집계만 수행한다(별도 집계 도메인 없음).
type StatsHandler struct {
	auth      domainauth.Service
	providers domainllmprovider.Service
}

// NewStatsHandler 는 StatsHandler 를 생성한다.
func NewStatsHandler(auth domainauth.Service, providers domainllmprovider.Service) *StatsHandler {
	return &StatsHandler{auth: auth, providers: providers}
}

type statMembers struct {
	Total  int            `json:"total"`
	ByRole map[string]int `json:"by_role"`
}

type statProviders struct {
	Total  int    `json:"total"`
	Active string `json:"active,omitempty"`
}

type statsResp struct {
	Members   statMembers   `json:"members"`
	Providers statProviders `json:"providers"`
}

// Get 은 대시보드 집계를 반환한다(admin↑; ListMembers 게이트가 강제).
func (h *StatsHandler) Get(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	actorID := claims.MemberID

	_, total, err := h.auth.ListMembers(r.Context(), actorID, domainauth.MemberFilter{})
	if err != nil {
		return authErrToHTTP(err)
	}

	byRole := make(map[string]int, 3)
	for _, roleID := range []int{domainauth.RoleIDUser, domainauth.RoleIDAdmin, domainauth.RoleIDSuperAdmin} {
		if _, n, e := h.auth.ListMembers(r.Context(), actorID, domainauth.MemberFilter{RoleID: roleID}); e == nil {
			byRole[domainauth.NameForRoleID(roleID)] = n
		}
	}

	providers, err := h.providers.List(r.Context(), actorID)
	if err != nil {
		return providerErrToHTTP(err)
	}
	active := ""
	for _, p := range providers {
		if p.IsActive {
			active = p.Name
			break
		}
	}

	writeJSON(w, http.StatusOK, statsResp{
		Members:   statMembers{Total: total, ByRole: byRole},
		Providers: statProviders{Total: len(providers), Active: active},
	})
	return nil
}
