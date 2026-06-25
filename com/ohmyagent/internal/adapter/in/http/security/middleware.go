package security

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rs/cors"
)

const (
	// healthPath: 헬스 체크 경로. LB 가 고빈도로 폴링하므로 액세스 로그에서 제외한다.
	healthPath = "/api/v1/health"
	// slowRequestThreshold: 이 시간 이상 걸린 요청은 Warn 으로 로깅(지연 가시성).
	slowRequestThreshold = 2 * time.Second
)

// statusRecorder 는 응답 상태코드와 본문 바이트 수를 캡처하는 ResponseWriter 래퍼다.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap 은 내부 ResponseWriter 를 노출한다. http.ResponseController 가 이를 통해
// Flush()·SetWriteDeadline() 등 옵셔널 기능에 도달한다(SSE 스트리밍 필수).
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// loggingMiddleware 는 요청을 구조화 로깅한다.
//   - 헬스 체크는 로그에서 제외(고빈도 폴링 노이즈 절감).
//   - request_id 를 부여/전파(X-Request-Id)하여 클라이언트~서버 로그 상관관계 확보.
//   - 상태/지연에 따라 레벨 분기(5xx=Error, 4xx 또는 느린 요청=Warn, 그 외 Info).
//   - 응답 바이트·정규화된 클라이언트 IP 등 운영에 유의미한 필드 포함.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}

		reqID := r.Header.Get("X-Request-Id")
		if reqID == "" {
			reqID = newRequestID()
		}
		w.Header().Set("X-Request-Id", reqID)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400 || dur >= slowRequestThreshold:
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "http request",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Float64("duration_ms", float64(dur.Microseconds())/1000.0),
			slog.String("client_ip", clientIP(r)),
		)
	})
}

// newRequestID 는 8바이트 랜덤 16진 요청 식별자를 만든다.
func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clientIP 는 프록시/LB 헤더(X-Forwarded-For → X-Real-Ip)를 우선해 원 클라이언트 IP 를 추출한다.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-Ip"); xr != "" {
		return strings.TrimSpace(xr)
	}
	return r.RemoteAddr
}

// Chain 은 미들웨어 체인을 구성한다: logging(CORS(handler)).
// allowedOrigins 가 비어 있으면 CORS 를 비활성화한다(로컬 dev).
func Chain(handler http.Handler, allowedOrigins []string) http.Handler {
	wrapped := handler
	if len(allowedOrigins) > 0 {
		c := cors.New(cors.Options{
			AllowedOrigins:   allowedOrigins,
			AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
		})
		wrapped = c.Handler(wrapped)
	}
	return loggingMiddleware(wrapped)
}
