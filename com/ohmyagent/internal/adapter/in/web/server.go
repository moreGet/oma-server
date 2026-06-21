// Package web 는 어드민 페이지(서버사이드 렌더링, htmx + html/template)를 제공하는 인바운드 어댑터다.
// JWT 를 HttpOnly 쿠키에 담아 인증하고, 핸들러가 application use case 를 직접 호출해 HTML 을 렌더링한다.
package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

//go:embed templates/*.html
var templatesFS embed.FS

const (
	sessionCookie = "admin_session"
	flashCookie   = "admin_flash"
	basePath      = "/admin"
)

// Server 는 어드민 웹 어댑터다. use case 를 직접 호출한다.
type Server struct {
	auth      domainauth.Service
	providers domainllmprovider.Service
	tokens    domainauth.TokenService
	cookieTTL time.Duration
	secure    bool // 운영(prod)에서 Secure 쿠키 플래그
	login     *template.Template
	pages     map[string]*template.Template
}

// NewServer 는 어드민 웹 서버를 생성하고 템플릿을 파싱한다.
func NewServer(auth domainauth.Service, providers domainllmprovider.Service, tokens domainauth.TokenService, cookieTTL time.Duration, secure bool) *Server {
	return &Server{
		auth:      auth,
		providers: providers,
		tokens:    tokens,
		cookieTTL: cookieTTL,
		secure:    secure,
		login:     template.Must(template.ParseFS(templatesFS, "templates/login.html")),
		pages:     parsePages(),
	}
}

// parsePages 는 layout + 각 페이지를 합쳐 페이지별 템플릿 세트를 만든다.
func parsePages() map[string]*template.Template {
	names := []string{"dashboard", "members", "providers", "account"}
	out := make(map[string]*template.Template, len(names))
	for _, n := range names {
		out[n] = template.Must(template.ParseFS(templatesFS, "templates/layout.html", "templates/"+n+".html"))
	}
	return out
}

// Register 는 어드민 라우트를 주어진 mux 에 등록한다(/admin 프리픽스).
// 웹 라우트는 자체 쿠키 인증을 사용하므로 SecureRouter(Bearer)와 독립적이다.
func (s *Server) Register(mux *http.ServeMux) {
	// 인증 불필요
	mux.HandleFunc("GET "+basePath+"/login", s.loginPage)
	mux.HandleFunc("POST "+basePath+"/login", s.loginSubmit)
	mux.HandleFunc("POST "+basePath+"/logout", s.logout)

	// 인증 필요(쿠키)
	mux.HandleFunc("GET "+basePath+"/{$}", s.authed(s.dashboard))
	mux.HandleFunc("GET "+basePath+"/members", s.authed(s.membersPage))
	mux.HandleFunc("POST "+basePath+"/members", s.authed(s.membersCreate))
	mux.HandleFunc("POST "+basePath+"/members/{id}/role", s.authed(s.memberChangeRole))
	mux.HandleFunc("POST "+basePath+"/members/{id}/active", s.authed(s.memberToggleActive))
	mux.HandleFunc("POST "+basePath+"/members/{id}/password", s.authed(s.memberResetPassword))
	mux.HandleFunc("POST "+basePath+"/members/{id}/delete", s.authed(s.memberDelete))

	mux.HandleFunc("GET "+basePath+"/providers", s.authed(s.providersPage))
	mux.HandleFunc("POST "+basePath+"/providers", s.authed(s.providerCreate))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/config", s.authed(s.providerUpdateConfig))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/activate", s.authed(s.providerActivate))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/test", s.authed(s.providerTest))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/delete", s.authed(s.providerDelete))

	mux.HandleFunc("GET "+basePath+"/account", s.authed(s.accountPage))
	mux.HandleFunc("POST "+basePath+"/account/password", s.authed(s.accountChangePassword))
}

// authed 는 쿠키의 JWT 를 검증하고 claims 를 context 에 주입한다. 실패 시 로그인으로 리다이렉트.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			s.redirect(w, r, basePath+"/login")
			return
		}
		claims, err := s.tokens.Parse(c.Value)
		if err != nil {
			s.clearSession(w)
			s.redirect(w, r, basePath+"/login")
			return
		}
		next(w, r.WithContext(security.WithClaims(r.Context(), claims)))
	}
}

// --- 쿠키 / 리다이렉트 / 플래시 헬퍼 ---

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(s.cookieTTL.Seconds()),
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) setFlash(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: msg, Path: basePath, MaxAge: 10, SameSite: http.SameSiteLaxMode})
}

func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: basePath, MaxAge: -1})
	return c.Value
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// render 는 layout 기반 페이지를 렌더링한다.
func (s *Server) render(w http.ResponseWriter, page string, data pageData) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		slog.Error("render page failed", "page", page, "error", err)
	}
}
