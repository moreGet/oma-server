package security

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/rs/cors"
)

// statusRecorder 는 응답 상태코드를 캡처하는 ResponseWriter 래퍼다.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

// Unwrap 은 내부 ResponseWriter 를 노출한다. http.ResponseController 가 이를 통해
// Flush()·SetWriteDeadline() 등 옵셔널 기능에 도달한다(SSE 스트리밍 필수).
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// loggingMiddleware 는 method/path/status/duration/remote 를 구조화 로깅한다.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
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
