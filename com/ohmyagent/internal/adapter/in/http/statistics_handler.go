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

	// 집계는 DB 에서 GROUP BY 로 끝낸다(쿼리 1회). 예전에는 멤버 행을 전량 가져와 메모리에서
	// 셌는데, 응답에 필요한 건 숫자 4개뿐이라 비용이 멤버 수에 비례할 이유가 없다.
	counts, total, err := h.auth.CountMembersByRole(r.Context(), actorID)
	if err != nil {
		return authErrToHTTP(err)
	}

	// 세 역할은 인원이 0이어도 키를 노출한다(응답 스키마 고정).
	byRole := map[string]int{
		domainauth.NameForRoleID(domainauth.RoleIDUser):       0,
		domainauth.NameForRoleID(domainauth.RoleIDAdmin):      0,
		domainauth.NameForRoleID(domainauth.RoleIDSuperAdmin): 0,
	}
	// 집계 결과를 NameForRoleID 로 접어 넣는다. 카탈로그 밖 role_id 를 "user" 로 취급하는
	// 폴백까지 예전 루프와 동일하게 유지하려는 것이다(FK 강제 여부에 기대지 않는다).
	for roleID, n := range counts {
		byRole[domainauth.NameForRoleID(roleID)] += n
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
