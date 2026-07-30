package httpin

import (
	"context"
	"net/http"

	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
)

// quotaStatusReader 는 본인 쿼터 현황 조회 포트다(*quotaapp.Service 가 충족).
type quotaStatusReader interface {
	Status(ctx context.Context, memberID string) (domainquota.Status, error)
}

// QuotaHandler 는 GET /api/v1/me/quota (본인 토큰 잔여량) 핸들러다.
type QuotaHandler struct {
	svc quotaStatusReader
}

// NewQuotaHandler 는 QuotaHandler 를 생성한다.
func NewQuotaHandler(svc quotaStatusReader) *QuotaHandler {
	return &QuotaHandler{svc: svc}
}

// Me 는 인증된 사용자의 일/주/월 쿼터 현황(한도·사용·잔여·사용률)을 반환한다.
func (h *QuotaHandler) Me(w http.ResponseWriter, r *http.Request) error {
	st, err := h.svc.Status(r.Context(), actorID(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toQuotaStatusDTO(st))
	return nil
}

type quotaWindowDTO struct {
	Window           string  `json:"window"` // day | week | month
	Period           string  `json:"period"` // 기간 키(YYYY-MM-DD / YYYY-Www / YYYY-MM)
	Limit            int     `json:"limit"`  // 0 = 무제한
	Used             int     `json:"used"`
	Remaining        int     `json:"remaining"`
	Unlimited        bool    `json:"unlimited"`
	PercentUsed      float64 `json:"percent_used"`
	PercentRemaining float64 `json:"percent_remaining"`
}

type quotaStatusDTO struct {
	Windows []quotaWindowDTO `json:"windows"`
}

func toQuotaStatusDTO(st domainquota.Status) quotaStatusDTO {
	out := quotaStatusDTO{Windows: make([]quotaWindowDTO, 0, len(st.Windows))}
	for _, ws := range st.Windows {
		out.Windows = append(out.Windows, quotaWindowDTO{
			Window:           string(ws.Window),
			Period:           periodKeyFor(st.Period, ws.Window),
			Limit:            ws.Limit,
			Used:             ws.Used,
			Remaining:        ws.Remaining,
			Unlimited:        ws.Unlimited,
			PercentUsed:      ws.PercentUsed,
			PercentRemaining: 100 - ws.PercentUsed, // 무제한이면 used%=0 → 100
		})
	}
	return out
}

func periodKeyFor(k domainquota.PeriodKeys, w domainquota.Window) string {
	switch w {
	case domainquota.Daily:
		return k.Daily
	case domainquota.Weekly:
		return k.Weekly
	default:
		return k.Monthly
	}
}
