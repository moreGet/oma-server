package security

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// apiKeyPrefix 는 서비스 계정 불투명 API 키 접두사다(확정 결정 3).
// domainserviceaccount.TokenPrefix 와 값이 동일해야 한다(역의존 회피용 로컬 상수 — 값 동기화 필수).
const apiKeyPrefix = "oma_sa_"

// APIKeyAuthenticator 는 서비스 계정 API 키(oma_sa_ 접두사)를 검증해 합성 Claims 를 반환하는 포트다.
// 실패(미존재/폐기/만료/계정폐기/DB오류)는 반드시 domainauth.ErrInvalidToken → 401. 절대 5xx 금지.
type APIKeyAuthenticator interface {
	Authenticate(ctx context.Context, raw string) (domainauth.Claims, error)
}

// SecureRouter 는 net/http ServeMux 를 래핑하여 Public/Secured 라우트 등록을 제공한다.
// Secured 라우트는 Bearer 토큰을 파싱하고 MinRole 게이트를 적용한 뒤 claims 를 context 에 주입한다.
type SecureRouter struct {
	mux        *http.ServeMux
	tokenSvc   domainauth.TokenService
	apiKeyAuth APIKeyAuthenticator // nil 이면 API키 경로 비활성(기존 JWT 전용 동작과 동일)
}

// RouterOption 은 SecureRouter 생성 옵션이다.
type RouterOption func(*SecureRouter)

// WithAPIKeyAuth 는 서비스 계정 API 키 인증기를 주입한다(oma_sa_ 접두사 토큰 처리 활성화).
func WithAPIKeyAuth(a APIKeyAuthenticator) RouterOption {
	return func(r *SecureRouter) { r.apiKeyAuth = a }
}

// NewSecureRouter 는 mux 와 토큰 서비스를 래핑한 SecureRouter 를 생성한다.
// 가변 옵션으로 API키 인증기를 주입할 수 있다(기존 2인자 호출은 그대로 컴파일된다 — JWT 전용).
func NewSecureRouter(mux *http.ServeMux, tokenSvc domainauth.TokenService, opts ...RouterOption) *SecureRouter {
	r := &SecureRouter{mux: mux, tokenSvc: tokenSvc}
	for _, o := range opts {
		o(r)
	}
	return r
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
		// oma_sa_ 접두사 + 인증기 주입 시에만 API키 경로로 분기한다(해시조회 부담은 이 경로만).
		// JWT 토큰은 절대 oma_sa_ 로 시작하지 않으므로 기존 JWT 경로는 바이트 동일하게 보존된다(무회귀).
		var claims domainauth.Claims
		var err error
		if r.apiKeyAuth != nil && strings.HasPrefix(raw, apiKeyPrefix) {
			claims, err = r.apiKeyAuth.Authenticate(req.Context(), raw)
		} else {
			claims, err = r.tokenSvc.Parse(raw)
		}
		if err != nil {
			// dev 디버깅용: 만료/서명불일치/형식오류 등 구체 사유를 남긴다(토큰 값은 미기록).
			slog.Debug("auth rejected", "event", "auth.reject", "reason", "invalid token", "error", err.Error(), "method", req.Method, "path", req.URL.Path)
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
			return
		}
		if claims.Level < opt.minLevel {
			// "level" 은 slog 내장 레벨 키와 충돌하므로 쓰지 않는다(JSON 중복 키 → 심각도가 덮어써진다).
			slog.Debug("auth rejected", "event", "auth.reject", "reason", "insufficient role", "role_level", int(claims.Level), "need", int(opt.minLevel), "method", req.Method, "path", req.URL.Path)
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
