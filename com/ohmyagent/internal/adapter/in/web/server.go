// Package web 는 어드민 페이지(서버사이드 렌더링, htmx + html/template)를 제공하는 인바운드 어댑터다.
// JWT 를 HttpOnly 쿠키에 담아 인증하고, 핸들러가 application use case 를 직접 호출해 HTML 을 렌더링한다.
package web

import (
	"context"
	"embed"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domainproject "aiagent/com/ohmyagent/internal/domain/project"
	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// sessionManager 는 어드민 세션(대화) 저장 설정·캡 관리 인터페이스다(*sessionapp.Manager 가 충족).
type sessionManager interface {
	GetSettings(ctx context.Context, actorID string) (domainproject.Settings, bool, error)
	UpdateSettings(ctx context.Context, cmd domainproject.UpdateSettingsCommand) error
	TestConnection(ctx context.Context, actorID string) error
	SetMemberLimit(ctx context.Context, actorID, memberID string, max int) error
	MemberLimits(ctx context.Context) (map[string]int, error)
	DefaultMaxSessions() int
}

// transcriptManager 는 어드민 대화이력 설정 화면이 사용하는 소비자 인터페이스다(*transcriptapp.Manager 가 충족).
type transcriptManager interface {
	GetSettings(ctx context.Context, actorID string) (domaintranscript.Settings, bool, error)
	UpdateSettings(ctx context.Context, cmd domaintranscript.UpdateCommand) error
	TestConnection(ctx context.Context, actorID string) error
}

// quotaManager 는 어드민 토큰 한도 관리가 사용하는 소비자 인터페이스다(*quotaapp.Service 가 충족).
type quotaManager interface {
	Snapshot(ctx context.Context) (domainquota.Snapshot, error)
	SetDefaultLimits(ctx context.Context, actorID string, l domainquota.Limits) error
	SetMemberLimits(ctx context.Context, actorID, memberID string, l domainquota.Limits) error
	ResetUsage(ctx context.Context, actorID, memberID string) error
}

//go:embed templates/*.html
var templatesFS embed.FS

const (
	sessionCookie = "admin_session"
	flashCookie   = "admin_flash"
	basePath      = "/admin"
	// flashTTLSeconds: 플래시 메시지 쿠키의 수명(초). 다음 페이지 1회 노출용.
	flashTTLSeconds = 10
)

// Server 는 어드민 웹 어댑터다. use case 를 직접 호출한다.
type Server struct {
	auth        domainauth.Service
	providers   domainllmprovider.Service
	transcripts transcriptManager
	quota       quotaManager
	sessions    sessionManager
	tokens      domainauth.TokenService
	cookieTTL   time.Duration
	secure      bool // 운영(prod)에서 Secure 쿠키 플래그
	login       *template.Template
	pages       map[string]*template.Template
}

// NewServer 는 어드민 웹 서버를 생성하고 템플릿을 파싱한다.
func NewServer(auth domainauth.Service, providers domainllmprovider.Service, transcripts transcriptManager, quota quotaManager, sessions sessionManager, tokens domainauth.TokenService, cookieTTL time.Duration, secure bool) *Server {
	return &Server{
		auth:        auth,
		providers:   providers,
		transcripts: transcripts,
		quota:       quota,
		sessions:    sessions,
		tokens:      tokens,
		cookieTTL:   cookieTTL,
		secure:      secure,
		login:       template.Must(template.ParseFS(templatesFS, "templates/login.html")),
		pages:       parsePages(),
	}
}

// templateFuncs 는 어드민 템플릿 공용 함수다.
//   - comma: 정수를 천 단위 구분 기호로 포맷(예: 1234567 → "1,234,567"). 표시 전용(입력 필드 값엔 미사용).
var templateFuncs = template.FuncMap{"comma": commaInt}

// commaInt 는 정수에 천 단위 콤마를 넣어 문자열로 반환한다.
func commaInt(n int) string {
	neg := n < 0
	s := strconv.Itoa(n)
	if neg {
		s = s[1:] // 부호 분리
	}
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// parsePages 는 layout + 각 페이지를 합쳐 페이지별 템플릿 세트를 만든다(공용 함수 주입).
func parsePages() map[string]*template.Template {
	names := []string{"dashboard", "members", "providers", "account", "transcripts", "sessions"}
	out := make(map[string]*template.Template, len(names))
	for _, n := range names {
		out[n] = template.Must(template.New("layout.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/layout.html", "templates/"+n+".html"))
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
	mux.HandleFunc("POST "+basePath+"/members/{id}/profile", s.authed(s.memberUpdateProfile))
	mux.HandleFunc("POST "+basePath+"/members/{id}/password", s.authed(s.memberResetPassword))
	mux.HandleFunc("POST "+basePath+"/members/{id}/token-limit", s.authed(s.memberSetTokenLimit))
	mux.HandleFunc("POST "+basePath+"/members/{id}/quota-reset", s.authed(s.memberResetQuota))
	mux.HandleFunc("POST "+basePath+"/members/{id}/session-limit", s.authed(s.memberSetSessionLimit))
	mux.HandleFunc("POST "+basePath+"/members/{id}/delete", s.authed(s.memberDelete))
	mux.HandleFunc("POST "+basePath+"/quota/default", s.authed(s.quotaSetDefault))

	mux.HandleFunc("GET "+basePath+"/providers", s.authed(s.providersPage))
	mux.HandleFunc("POST "+basePath+"/providers", s.authed(s.providerCreate))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/config", s.authed(s.providerUpdateConfig))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/activate", s.authed(s.providerActivate))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/test", s.authed(s.providerTest))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/delete", s.authed(s.providerDelete))

	mux.HandleFunc("GET "+basePath+"/account", s.authed(s.accountPage))

	mux.HandleFunc("GET "+basePath+"/transcripts", s.authed(s.transcriptsPage))
	mux.HandleFunc("POST "+basePath+"/transcripts", s.authed(s.transcriptsUpdate))
	mux.HandleFunc("POST "+basePath+"/transcripts/test", s.authed(s.transcriptsTest))

	mux.HandleFunc("GET "+basePath+"/sessions", s.authed(s.sessionsPage))
	mux.HandleFunc("POST "+basePath+"/sessions", s.authed(s.sessionsUpdate))
	mux.HandleFunc("POST "+basePath+"/sessions/test", s.authed(s.sessionsTest))
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

// setFlash 는 성공 플래시 메시지를 설정한다.
func (s *Server) setFlash(w http.ResponseWriter, msg string) { s.writeFlash(w, 'S', msg) }

// setFlashError 는 오류 플래시 메시지를 설정한다(토스트가 빨간색으로 표시).
func (s *Server) setFlashError(w http.ResponseWriter, msg string) { s.writeFlash(w, 'E', msg) }

// writeFlash 는 [레벨바이트 + 메시지]를 base64 로 인코딩해 쿠키에 담는다.
// 한글 등 비ASCII 가 쿠키 값에서 잘리지 않도록(net/http 의 쿠키 sanitizer 회피) base64 를 쓴다.
func (s *Server) writeFlash(w http.ResponseWriter, kind byte, msg string) {
	enc := base64.StdEncoding.EncodeToString(append([]byte{kind}, msg...))
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: enc, Path: basePath, MaxAge: flashTTLSeconds, SameSite: http.SameSiteLaxMode})
}

// popFlash 는 플래시 메시지와 오류 여부를 읽고 쿠키를 제거한다(1회성).
func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) (string, bool) {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: basePath, MaxAge: -1})
	raw, decErr := base64.StdEncoding.DecodeString(c.Value)
	if decErr != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw[1:]), raw[0] == 'E'
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
