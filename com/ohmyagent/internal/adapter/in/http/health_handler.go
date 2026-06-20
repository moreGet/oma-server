package httpin

import (
	"context"
	"net/http"
	"time"
)

const healthPingTimeout = 2 * time.Second

// pinger 는 헬스체크용 DB ping 능력의 최소 인터페이스다(*sql.DB 가 충족).
type pinger interface {
	PingContext(ctx context.Context) error
}

// HealthHandler 는 GET /api/v1/health (서버 연결/헬스 체크) 핸들러다. 인증 불필요(Public).
type HealthHandler struct {
	db  pinger
	now func() time.Time
}

// NewHealthHandler 는 HealthHandler 를 생성한다. db 가 nil 이면 DB 체크를 건너뛴다.
func NewHealthHandler(db pinger) *HealthHandler {
	return &HealthHandler{db: db, now: time.Now}
}

type healthResp struct {
	Status   string `json:"status"`   // ok | degraded
	Time     string `json:"time"`     // RFC3339
	Database string `json:"database"` // ok | down | skipped
}

// Check 는 헬스 상태를 반환한다(항상 200; DB 다운 시 status=degraded).
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) error {
	status, dbStatus := "ok", "skipped"
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
		defer cancel()
		if err := h.db.PingContext(ctx); err != nil {
			status, dbStatus = "degraded", "down"
		} else {
			dbStatus = "ok"
		}
	}
	writeJSON(w, http.StatusOK, healthResp{
		Status:   status,
		Time:     h.now().UTC().Format(time.RFC3339),
		Database: dbStatus,
	})
	return nil
}
