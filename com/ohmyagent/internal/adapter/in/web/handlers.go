package web

import (
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
		if _, total, err := s.auth.ListMembers(r.Context(), actorID(r), domainauth.MemberFilter{}); err == nil {
			dv.MemberTotal = total
		}
		for _, roleID := range []int{domainauth.RoleIDUser, domainauth.RoleIDAdmin, domainauth.RoleIDSuperAdmin} {
			if _, n, err := s.auth.ListMembers(r.Context(), actorID(r), domainauth.MemberFilter{RoleID: roleID}); err == nil {
				dv.ByRole = append(dv.ByRole, roleCount{Name: domainauth.NameForRoleID(roleID), Count: n})
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
	roles, _ := s.auth.ListRoles(r.Context())
	mv := membersView{Members: toMemberViews(members), Roles: toRoleViews(roles)}
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
		s.setFlash(w, "연결 테스트 실패: "+err.Error())
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
func (s *Server) flashResult(w http.ResponseWriter, err error, okMsg string) {
	if err != nil {
		s.setFlash(w, "오류: "+err.Error())
		return
	}
	s.setFlash(w, okMsg)
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
