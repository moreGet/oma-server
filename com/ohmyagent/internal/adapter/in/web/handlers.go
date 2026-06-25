package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// --- 뷰 데이터 ---

type userView struct {
	Username string
	Role     string
	Level    int
}

type pageData struct {
	Title            string
	Active           string // 현재 활성 nav 키
	User             userView
	CanManageMembers bool // admin↑
	CanDeleteMembers bool // super_admin
	CanManage        bool // admin↑ (provider 등 쓰기)
	Flash            string
	Data             any
}

type memberView struct {
	ID        string
	Username  string
	Role      string
	Level     int
	Active    bool
	CreatedAt string
}

type roleView struct {
	ID   int
	Name string
}

type providerView struct {
	ID        string
	Name      string
	Type      string
	Model     string
	Endpoint  string
	APIKeyEnv string
	MaxTokens int
	Active    bool
}

type roleCount struct {
	Name  string
	Count int
}

type dashboardView struct {
	ShowMembers    bool
	MemberTotal    int
	ByRole         []roleCount
	ProviderTotal  int
	ActiveProvider string
}

type membersView struct {
	Members []memberView
	Roles   []roleView
}

type providersView struct {
	Providers []providerView
}

// --- 공통 헬퍼 ---

func (s *Server) base(r *http.Request, w http.ResponseWriter, title, active string) pageData {
	claims, _ := security.ClaimsFrom(r.Context())
	lvl := int(claims.Level)
	return pageData{
		Title:            title,
		Active:           active,
		User:             userView{Username: claims.Username, Role: domainauth.NameForRoleID(lvl + 1), Level: lvl},
		CanManageMembers: claims.Level >= domainauth.RoleLevelAdmin,
		CanDeleteMembers: claims.Level >= domainauth.RoleLevelSuperAdmin,
		CanManage:        claims.Level >= domainauth.RoleLevelAdmin,
		Flash:            s.popFlash(w, r),
	}
}

func actorID(r *http.Request) string {
	claims, _ := security.ClaimsFrom(r.Context())
	return claims.MemberID
}

// --- 로그인 / 로그아웃 ---

func (s *Server) renderLogin(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = s.login.Execute(w, map[string]any{"Error": errMsg})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if _, perr := s.tokens.Parse(c.Value); perr == nil {
			s.redirect(w, r, basePath+"/")
			return
		}
	}
	s.renderLogin(w, http.StatusOK, "")
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "invalid form")
		return
	}
	token, _, err := s.auth.Login(r.Context(), domainauth.LoginCommand{
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
	})
	if err != nil {
		s.renderLogin(w, http.StatusUnauthorized, "아이디 또는 비밀번호가 올바르지 않습니다.")
		return
	}
	s.setSession(w, token)
	s.redirect(w, r, basePath+"/")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	s.redirect(w, r, basePath+"/login")
}

// --- 대시보드 ---

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "대시보드", "dashboard")
	dv := dashboardView{}

	if pd.CanManageMembers {
		dv.ShowMembers = true
		// 멤버를 한 번만 조회하고 역할별 카운트는 메모리에서 집계한다(역할별 추가 쿼리 3회 제거).
		if members, total, err := s.auth.ListMembers(r.Context(), actorID(r), domainauth.MemberFilter{}); err == nil {
			dv.MemberTotal = total
			counts := make(map[int]int, 3)
			for _, m := range members {
				counts[m.Role.ID]++
			}
			for _, roleID := range []int{domainauth.RoleIDUser, domainauth.RoleIDAdmin, domainauth.RoleIDSuperAdmin} {
				dv.ByRole = append(dv.ByRole, roleCount{Name: domainauth.NameForRoleID(roleID), Count: counts[roleID]})
			}
		}
	}
	if providers, err := s.providers.List(r.Context(), actorID(r)); err == nil {
		dv.ProviderTotal = len(providers)
		for _, p := range providers {
			if p.IsActive {
				dv.ActiveProvider = p.Name
				break
			}
		}
	}
	pd.Data = dv
	s.render(w, "dashboard", pd)
}

// --- 멤버 관리 ---

func (s *Server) membersPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "멤버 관리", "members")
	members, _, err := s.auth.ListMembers(r.Context(), actorID(r), domainauth.MemberFilter{Limit: 100})
	if err != nil {
		s.setFlash(w, "멤버 목록을 볼 권한이 없습니다.")
		s.redirect(w, r, basePath+"/")
		return
	}
	// 역할 드롭다운은 actor 가 제어 가능한(자기보다 낮은 레벨) 역할만 노출한다.
	// 인가 규칙 CanControl(actor>target)과 UI 를 일치시켜, 할당 불가능한 역할을
	// 골라 생성/변경이 매번 permission denied 로 실패하는 문제를 방지한다.
	roles, _ := s.auth.ListRoles(r.Context())
	controllable := make([]domainauth.Role, 0, len(roles))
	for _, role := range roles {
		if int(role.Level) < pd.User.Level {
			controllable = append(controllable, role)
		}
	}
	mv := membersView{Members: toMemberViews(members), Roles: toRoleViews(controllable)}
	pd.Data = mv
	s.render(w, "members", pd)
}

func (s *Server) membersCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	roleID, _ := strconv.Atoi(r.FormValue("role_id"))
	_, err := s.auth.CreateMember(r.Context(), domainauth.CreateMemberCommand{
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
		RoleID:   roleID,
		ActorID:  actorID(r),
	})
	s.flashResult(w, err, "멤버가 생성되었습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberChangeRole(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	roleID, _ := strconv.Atoi(r.FormValue("role_id"))
	_, err := s.auth.ChangeRole(r.Context(), domainauth.ChangeRoleCommand{
		ActorID:  actorID(r),
		TargetID: r.PathValue("id"),
		RoleID:   roleID,
	})
	s.flashResult(w, err, "역할이 변경되었습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberToggleActive(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	active := r.FormValue("active") == "true"
	_, err := s.auth.SetActive(r.Context(), domainauth.SetActiveCommand{
		ActorID:  actorID(r),
		TargetID: r.PathValue("id"),
		Active:   active,
	})
	s.flashResult(w, err, "활성 상태가 변경되었습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberResetPassword(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.auth.ResetPassword(r.Context(), actorID(r), r.PathValue("id"), r.FormValue("new_password"))
	s.flashResult(w, err, "비밀번호가 리셋되었습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberDelete(w http.ResponseWriter, r *http.Request) {
	err := s.auth.DeleteMember(r.Context(), actorID(r), r.PathValue("id"))
	s.flashResult(w, err, "멤버가 삭제되었습니다.")
	s.redirect(w, r, basePath+"/members")
}

// --- Provider 관리 ---

func (s *Server) providersPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "LLM Provider", "providers")
	providers, err := s.providers.List(r.Context(), actorID(r))
	if err != nil {
		s.setFlash(w, "Provider 목록을 불러오지 못했습니다.")
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = providersView{Providers: toProviderViews(providers)}
	s.render(w, "providers", pd)
}

func (s *Server) providerCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	maxTokens, _ := strconv.Atoi(r.FormValue("max_tokens"))
	_, err := s.providers.Create(r.Context(), domainllmprovider.CreateCommand{
		Name:         r.FormValue("name"),
		ProviderType: domainllmprovider.ProviderType(r.FormValue("provider_type")),
		IsActive:     r.FormValue("is_active") == "on",
		Config: domainllmprovider.ProviderConfig{
			Model:     r.FormValue("model"),
			Endpoint:  r.FormValue("endpoint"),
			APIKeyEnv: r.FormValue("api_key_env"),
			MaxTokens: maxTokens,
		},
		ActorID: actorID(r),
	})
	s.flashResult(w, err, "Provider 가 생성되었습니다.")
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerUpdateConfig(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	maxTokens, _ := strconv.Atoi(r.FormValue("max_tokens"))
	_, err := s.providers.UpdateConfig(r.Context(), domainllmprovider.UpdateConfigCommand{
		ID: r.PathValue("id"),
		Config: domainllmprovider.ProviderConfig{
			Model:     r.FormValue("model"),
			Endpoint:  r.FormValue("endpoint"),
			APIKeyEnv: r.FormValue("api_key_env"),
			MaxTokens: maxTokens,
		},
		ActorID: actorID(r),
	})
	s.flashResult(w, err, "Provider 설정이 갱신되었습니다.")
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerActivate(w http.ResponseWriter, r *http.Request) {
	err := s.providers.Activate(r.Context(), domainllmprovider.ActivateCommand{ID: r.PathValue("id"), ActorID: actorID(r)})
	s.flashResult(w, err, "활성 Provider 가 변경되었습니다.")
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerTest(w http.ResponseWriter, r *http.Request) {
	if err := s.providers.TestConnection(r.Context(), actorID(r), r.PathValue("id")); err != nil {
		// 연결 진단용 상세는 upstream 에러일 때만 노출(시크릿은 도메인 검증으로 사전 차단됨).
		// 그 외(권한/대상 없음)는 친화 메시지로 매핑. 원시 에러는 use case 가 서버 로깅함.
		msg := webErrorMessage(err)
		if errors.Is(err, domainllmprovider.ErrUpstream) {
			msg = err.Error()
		}
		s.setFlash(w, "연결 테스트 실패: "+msg)
	} else {
		s.setFlash(w, "연결 테스트 성공 ✓")
	}
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerDelete(w http.ResponseWriter, r *http.Request) {
	err := s.providers.Delete(r.Context(), domainllmprovider.DeleteCommand{ID: r.PathValue("id"), ActorID: actorID(r)})
	s.flashResult(w, err, "Provider 가 삭제되었습니다.")
	s.redirect(w, r, basePath+"/providers")
}

// --- 내 계정 ---

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "내 계정", "account")
	s.render(w, "account", pd)
}

func (s *Server) accountChangePassword(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.auth.ChangePassword(r.Context(), actorID(r), r.FormValue("old_password"), r.FormValue("new_password"))
	s.flashResult(w, err, "비밀번호가 변경되었습니다.")
	s.redirect(w, r, basePath+"/account")
}

// flashResult 는 use case 결과를 플래시 메시지로 변환한다.
// 실패 시 원시 에러를 사용자에게 노출하지 않고 친화적 메시지로 매핑하며,
// 운영/감사를 위해 원시 에러는 서버 로그로 남긴다(시크릿 누출 방지).
func (s *Server) flashResult(w http.ResponseWriter, err error, okMsg string) {
	if err != nil {
		slog.Warn("admin action failed", "event", "admin.action", "error", err)
		s.setFlash(w, webErrorMessage(err))
		return
	}
	s.setFlash(w, okMsg)
}

// webErrorMessage 는 도메인 에러를 사용자 친화 한글 메시지로 매핑한다(글로벌 예외 처리).
// 검증 에러 메시지는 사용자에게 보여주도록 설계된 안내이므로 그대로 노출하고,
// 그 외 알 수 없는 에러는 내부 상세를 숨기고 일반 메시지로 대체한다.
func webErrorMessage(err error) string {
	var ave *domainauth.ErrValidation
	var pve *domainllmprovider.ErrValidation
	switch {
	case errors.As(err, &ave):
		return ave.Msg
	case errors.As(err, &pve):
		return pve.Msg
	case errors.Is(err, domainauth.ErrPermission):
		return "이 작업을 수행할 권한이 없습니다."
	case errors.Is(err, domainauth.ErrInvalidCredentials):
		return "현재 비밀번호가 올바르지 않습니다."
	case errors.Is(err, domainauth.ErrConflict), errors.Is(err, domainllmprovider.ErrConflict):
		return "이미 존재하는 항목입니다."
	case errors.Is(err, domainauth.ErrNotFound), errors.Is(err, domainllmprovider.ErrNotFound):
		return "대상을 찾을 수 없습니다."
	default:
		return "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해주세요."
	}
}

// --- 매핑 ---

func toMemberViews(ms []domainauth.Member) []memberView {
	out := make([]memberView, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberView{
			ID: m.ID, Username: m.Username, Role: m.Role.Name, Level: int(m.Role.Level),
			Active: m.Active, CreatedAt: m.CreatedAt.Format(time.RFC3339),
		})
	}
	return out
}

func toRoleViews(rs []domainauth.Role) []roleView {
	out := make([]roleView, 0, len(rs))
	for _, r := range rs {
		out = append(out, roleView{ID: r.ID, Name: r.Name})
	}
	return out
}

func toProviderViews(ps []domainllmprovider.LLMProvider) []providerView {
	out := make([]providerView, 0, len(ps))
	for _, p := range ps {
		out = append(out, providerView{
			ID: p.ID, Name: p.Name, Type: string(p.ProviderType), Model: p.Config.Model,
			Endpoint: p.Config.Endpoint, APIKeyEnv: p.Config.APIKeyEnv, MaxTokens: p.Config.MaxTokens, Active: p.IsActive,
		})
	}
	return out
}
