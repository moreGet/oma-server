package security

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// SecureRouter 는 net/http ServeMux 를 래핑하여 Public/Secured 라우트 등록을 제공한다.
// Secured 라우트는 Bearer 토큰을 파싱하고 MinRole 게이트를 적용한 뒤 claims 를 context 에 주입한다.
type SecureRouter struct {
	mux      *http.ServeMux
	tokenSvc domainauth.TokenService
}

// NewSecureRouter 는 mux 와 토큰 서비스를 래핑한 SecureRouter 를 생성한다.
func NewSecureRouter(mux *http.ServeMux, tokenSvc domainauth.TokenService) *SecureRouter {
	return &SecureRouter{mux: mux, tokenSvc: tokenSvc}
}

// Mux 는 내부 ServeMux 를 반환한다(미들웨어 체인 구성용).
func (r *SecureRouter) Mux() *http.ServeMux { return r.mux }

// Option 은 Secured 라우트의 인가 옵션이다.
type Option struct {
	minLevel domainauth.RoleLevel
}

// MinRole 은 라우트의 최소 역할 레벨을 지정하는 옵션이다.
func MinRole(level domainauth.RoleLevel) func(*Option) {
	return func(o *Option) { o.minLevel = level }
}

// Public 은 인증 불필요 라우트를 등록한다(Go 1.22+ "METHOD /path" 패턴).
func (r *SecureRouter) Public(pattern string, handler http.HandlerFunc) {
	r.mux.HandleFunc(pattern, handler)
}

// Secured 는 인증·인가가 필요한 라우트를 등록한다.
// Bearer 파싱 실패 → 401, claims.Level < minLevel → 403, 통과 시 claims 를 context 에 주입한다.
func (r *SecureRouter) Secured(pattern string, handler http.HandlerFunc, opts ...func(*Option)) {
	var opt Option
	for _, apply := range opts {
		apply(&opt)
	}
	r.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		raw := extractBearer(req)
		if raw == "" {
			slog.Debug("auth rejected", "event", "auth.reject", "reason", "missing bearer token", "method", req.Method, "path", req.URL.Path)
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
			return
		}
		claims, err := r.tokenSvc.Parse(raw)
		if err != nil {
			// dev 디버깅용: 만료/서명불일치/형식오류 등 구체 사유를 남긴다(토큰 값은 미기록).
			slog.Debug("auth rejected", "event", "auth.reject", "reason", "invalid token", "error", err.Error(), "method", req.Method, "path", req.URL.Path)
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
			return
		}
		if claims.Level < opt.minLevel {
			slog.Debug("auth rejected", "event", "auth.reject", "reason", "insufficient role", "level", int(claims.Level), "need", int(opt.minLevel), "method", req.Method, "path", req.URL.Path)
			writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "insufficient role")
			return
		}
		handler(w, req.WithContext(withClaims(req.Context(), claims)))
	})
}

// extractBearer 는 Authorization 헤더에서 Bearer 토큰을 추출한다.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// writeAuthError 는 인증/인가 실패를 AppError envelope 형식으로 응답한다.
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
