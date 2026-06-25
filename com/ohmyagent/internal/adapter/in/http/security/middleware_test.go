package security

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoggingMiddleware_HealthSkippedAndRequestID(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := loggingMiddleware(next)

	t.Run("health path is passed through without request id (logging skipped)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, healthPath, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Header().Get("X-Request-Id"))
	})

	t.Run("non-health path gets a generated request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		assert.NotEmpty(t, w.Header().Get("X-Request-Id"))
	})

	t.Run("incoming X-Request-Id is preserved for correlation", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
		req.Header.Set("X-Request-Id", "client-correlation-123")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		assert.Equal(t, "client-correlation-123", w.Header().Get("X-Request-Id"))
	})
}

func TestClientIP(t *testing.T) {
	t.Run("falls back to RemoteAddr", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		assert.Equal(t, "10.0.0.1:1234", clientIP(r))
	})
	t.Run("prefers first X-Forwarded-For entry", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
		assert.Equal(t, "203.0.113.5", clientIP(r))
	})
	t.Run("uses X-Real-Ip when no XFF", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Real-Ip", "198.51.100.2")
		assert.Equal(t, "198.51.100.2", clientIP(r))
	})
}

func TestNewRequestID(t *testing.T) {
	a, b := newRequestID(), newRequestID()
	assert.Len(t, a, 16) // 8 bytes hex
	assert.NotEqual(t, a, b)
}
