package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domainproject "aiagent/com/ohmyagent/internal/domain/project"
	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// uiTimeFormat 은 어드민 웹 UI 시간 표기 포맷이다(읽기 쉬운 UTC, ISO 'T' 미사용).
// JSON API 는 별도로 RFC3339(T 규격) 를 유지한다 — 여기는 화면 표시 전용.
const uiTimeFormat = "2006-01-02 15:04:05"

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
	FlashError       bool // true 면 오류 토스트(빨강), false 면 성공 토스트(초록)
	Data             any
}

type memberView struct {
	ID           string
	Username     string
	Role         string
	Level        int
	Active       bool
	CreatedAt    string
	Email        string
	DisplayName  string
	Organization string
	// 토큰 한도(멤버 오버라이드, 0=전역 기본값) — 모달 입력용.
	DailyLimit   int
	WeeklyLimit  int
	MonthlyLimit int
	DailyUsed    int
	WeeklyUsed   int
	MonthlyUsed  int
	// Quota 는 표시용 일/주/월 사용률(유효 한도 기준). 목록 진행바에 사용.
	Quota []memberQuotaView
	// SessionLimit 는 멤버별 최대 세션 수 오버라이드(0 = 전역 기본값).
	SessionLimit int
}

// memberQuotaView 는 한 윈도우의 표시용 사용률이다(유효 한도 = 오버라이드>0 ? 오버라이드 : 전역 기본).
type memberQuotaView struct {
	Label     string // 일 | 주 | 월
	Used      int
	Limit     int // 유효 한도(0 = 무제한)
	Unlimited bool
	PctUsed   int // 0..100
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
	APIKeySet bool // 직접 저장된(암호화) API 키 존재 여부(마스킹 표시용)
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
	Default domainquota.Limits     // 전역 기본 한도(일·주·월, 0 = 무제한)
	Keys    domainquota.PeriodKeys // 현재 기간 키(표시용)
}

type providersView struct {
	Providers []providerView
}

// --- 공통 헬퍼 ---

func (s *Server) base(r *http.Request, w http.ResponseWriter, title, active string) pageData {
	claims, _ := security.ClaimsFrom(r.Context())
	lvl := int(claims.Level)
	flash, flashErr := s.popFlash(w, r)
	return pageData{
		Title:            title,
		Active:           active,
		User:             userView{Username: claims.Username, Role: domainauth.NameForRoleID(lvl + 1), Level: lvl},
		CanManageMembers: claims.Level >= domainauth.RoleLevelAdmin,
		CanDeleteMembers: claims.Level >= domainauth.RoleLevelSuperAdmin,
		CanManage:        claims.Level >= domainauth.RoleLevelAdmin,
		Flash:            flash,
		FlashError:       flashErr,
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
		s.setFlashError(w, "멤버 목록을 볼 권한이 없습니다.")
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
	memberViews := toMemberViews(members)
	mv := membersView{Members: memberViews, Roles: toRoleViews(controllable)}
	// 토큰 쿼터: 전역 기본값 + 이번 일·주·월 멤버별 한도/사용량을 함께 표시한다.
	if s.quota != nil {
		if snap, err := s.quota.Snapshot(r.Context()); err == nil {
			mv.Default = snap.Default
			mv.Keys = snap.Keys
			for i := range memberViews {
				ov := snap.Limits[memberViews[i].ID]
				use := snap.Usage[memberViews[i].ID]
				memberViews[i].DailyLimit, memberViews[i].WeeklyLimit, memberViews[i].MonthlyLimit = ov.Daily, ov.Weekly, ov.Monthly
				memberViews[i].DailyUsed, memberViews[i].WeeklyUsed, memberViews[i].MonthlyUsed = use.Daily, use.Weekly, use.Monthly
				memberViews[i].Quota = []memberQuotaView{
					quotaWin("일", ov.Daily, snap.Default.Daily, use.Daily),
					quotaWin("주", ov.Weekly, snap.Default.Weekly, use.Weekly),
					quotaWin("월", ov.Monthly, snap.Default.Monthly, use.Monthly),
				}
			}
		}
	}
	// 멤버별 최대 세션 수 오버라이드(모달 입력용).
	if s.sessions != nil {
		if limits, err := s.sessions.MemberLimits(r.Context()); err == nil {
			for i := range memberViews {
				memberViews[i].SessionLimit = limits[memberViews[i].ID]
			}
		}
	}
	pd.Data = mv
	s.render(w, "members", pd)
}

func (s *Server) membersCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	roleID, _ := strconv.Atoi(r.FormValue("role_id"))
	_, err := s.auth.CreateMember(r.Context(), domainauth.CreateMemberCommand{
		Username:     r.FormValue("username"),
		Password:     r.FormValue("password"),
		RoleID:       roleID,
		ActorID:      actorID(r),
		Email:        r.FormValue("email"),
		DisplayName:  r.FormValue("display_name"),
		Organization: r.FormValue("organization"),
	})
	s.flashResult(w, err, "멤버를 생성했습니다.")
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
	s.flashResult(w, err, "역할을 변경했습니다.")
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
	s.flashResult(w, err, "활성 상태를 변경했습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberUpdateProfile(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	_, err := s.auth.UpdateProfile(r.Context(), domainauth.UpdateProfileCommand{
		ActorID:      actorID(r),
		TargetID:     r.PathValue("id"),
		Email:        r.FormValue("email"),
		DisplayName:  r.FormValue("display_name"),
		Organization: r.FormValue("organization"),
	})
	s.flashResult(w, err, "프로필을 변경했습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberResetPassword(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.auth.ResetPassword(r.Context(), actorID(r), r.PathValue("id"), r.FormValue("new_password"))
	s.flashResult(w, err, "비밀번호를 초기화했습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberDelete(w http.ResponseWriter, r *http.Request) {
	err := s.auth.DeleteMember(r.Context(), actorID(r), r.PathValue("id"))
	s.flashResult(w, err, "멤버를 삭제했습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) memberSetTokenLimit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.quota.SetMemberLimits(r.Context(), actorID(r), r.PathValue("id"), formLimits(r))
	s.flashResult(w, err, "토큰 한도를 변경했습니다.")
	s.redirect(w, r, basePath+"/members")
}

func (s *Server) quotaSetDefault(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.quota.SetDefaultLimits(r.Context(), actorID(r), formLimits(r))
	s.flashResult(w, err, "전역 기본 토큰 한도를 변경했습니다.")
	s.redirect(w, r, basePath+"/members")
}

// quotaWin 은 표시용 사용률 뷰를 만든다(유효 한도 = 오버라이드>0 ? 오버라이드 : 전역 기본).
func quotaWin(label string, override, def, used int) memberQuotaView {
	limit := override
	if limit <= 0 {
		limit = def
	}
	v := memberQuotaView{Label: label, Used: used, Limit: limit, Unlimited: limit <= 0}
	if limit > 0 {
		if pct := used * 100 / limit; pct > 100 {
			v.PctUsed = 100
		} else {
			v.PctUsed = pct
		}
	}
	return v
}

func (s *Server) memberResetQuota(w http.ResponseWriter, r *http.Request) {
	err := s.quota.ResetUsage(r.Context(), actorID(r), r.PathValue("id"))
	s.flashResult(w, err, "사용량을 초기화했습니다.")
	s.redirect(w, r, basePath+"/members")
}

// formLimits 는 폼에서 일/주/월 한도를 읽는다(빈/비정상 값은 0).
func formLimits(r *http.Request) domainquota.Limits {
	d, _ := strconv.Atoi(r.FormValue("daily_limit"))
	wk, _ := strconv.Atoi(r.FormValue("weekly_limit"))
	m, _ := strconv.Atoi(r.FormValue("monthly_limit"))
	return domainquota.Limits{Daily: d, Weekly: wk, Monthly: m}
}

// --- Provider 관리 ---

func (s *Server) providersPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "LLM Provider", "providers")
	providers, err := s.providers.List(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, "Provider 목록을 불러오지 못했습니다.")
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
			APIKey:    r.FormValue("api_key"), // 평문 입력 → 유스케이스가 암호화 저장
			MaxTokens: maxTokens,
		},
		ActorID: actorID(r),
	})
	s.flashResult(w, err, "Provider를 등록했습니다.")
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
			APIKey:    r.FormValue("api_key"), // 평문 입력 → 유스케이스가 암호화 저장
			MaxTokens: maxTokens,
		},
		ActorID: actorID(r),
	})
	s.flashResult(w, err, "Provider 설정을 저장했습니다.")
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerActivate(w http.ResponseWriter, r *http.Request) {
	err := s.providers.Activate(r.Context(), domainllmprovider.ActivateCommand{ID: r.PathValue("id"), ActorID: actorID(r)})
	s.flashResult(w, err, "활성 Provider를 변경했습니다.")
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
		s.setFlashError(w, "연결 테스트 실패: "+msg)
	} else {
		s.setFlash(w, "연결 테스트에 성공했습니다.")
	}
	s.redirect(w, r, basePath+"/providers")
}

func (s *Server) providerDelete(w http.ResponseWriter, r *http.Request) {
	err := s.providers.Delete(r.Context(), domainllmprovider.DeleteCommand{ID: r.PathValue("id"), ActorID: actorID(r)})
	s.flashResult(w, err, "Provider를 삭제했습니다.")
	s.redirect(w, r, basePath+"/providers")
}

// --- 내 계정 ---

type accountView struct {
	ID           string
	Email        string
	DisplayName  string
	Organization string
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "내 계정", "account")
	if member, err := s.auth.GetMember(r.Context(), actorID(r), actorID(r)); err == nil {
		pd.Data = accountView{ID: member.ID, Email: member.Email, DisplayName: member.DisplayName, Organization: member.Organization}
	}
	s.render(w, "account", pd)
}

// --- 대화 이력 저장 설정 ---

type transcriptView struct {
	Enabled          bool
	Backend          string // db | file | s3
	FileDir          string
	S3Endpoint       string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3UseSSL         bool
	HasSecret        bool // S3 시크릿 저장 여부(마스킹 표시용)
	RetentionDays    int
	StripAttachments bool
}

func (s *Server) transcriptsPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "대화 이력 저장", "transcripts")
	settings, hasSecret, err := s.transcripts.GetSettings(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = transcriptView{
		Enabled: settings.Enabled, Backend: string(settings.Backend), FileDir: settings.FileDir,
		S3Endpoint: settings.S3Endpoint, S3Bucket: settings.S3Bucket, S3Region: settings.S3Region,
		S3AccessKey: settings.S3AccessKey, S3UseSSL: settings.S3UseSSL, HasSecret: hasSecret,
		RetentionDays: settings.RetentionDays, StripAttachments: settings.StripAttachments,
	}
	s.render(w, "transcripts", pd)
}

func (s *Server) transcriptsUpdate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	retentionDays, _ := strconv.Atoi(r.FormValue("retention_days"))
	err := s.transcripts.UpdateSettings(r.Context(), domaintranscript.UpdateCommand{
		ActorID: actorID(r),
		Settings: domaintranscript.Settings{
			Enabled:          r.FormValue("enabled") == "on",
			Backend:          domaintranscript.Backend(r.FormValue("backend")),
			FileDir:          r.FormValue("file_dir"),
			S3Endpoint:       r.FormValue("s3_endpoint"),
			S3Bucket:         r.FormValue("s3_bucket"),
			S3Region:         r.FormValue("s3_region"),
			S3AccessKey:      r.FormValue("s3_access_key"),
			S3SecretKey:      r.FormValue("s3_secret_key"),
			S3UseSSL:         r.FormValue("s3_use_ssl") == "on",
			RetentionDays:    retentionDays,
			StripAttachments: r.FormValue("strip_attachments") == "on",
		},
	})
	s.flashResult(w, err, "대화 이력 설정을 저장했습니다.")
	s.redirect(w, r, basePath+"/transcripts")
}

func (s *Server) transcriptsTest(w http.ResponseWriter, r *http.Request) {
	if err := s.transcripts.TestConnection(r.Context(), actorID(r)); err != nil {
		s.setFlashError(w, "연결 테스트 실패: "+err.Error())
	} else {
		s.setFlash(w, "연결 테스트에 성공했습니다.")
	}
	s.redirect(w, r, basePath+"/transcripts")
}

// --- 세션(대화) 저장 설정 ---

type sessionView struct {
	Backend            string // db | file | s3
	FileDir            string
	S3Endpoint         string
	S3Bucket           string
	S3Region           string
	S3AccessKey        string
	S3UseSSL           bool
	HasSecret          bool
	DefaultMaxSessions int
}

func (s *Server) sessionsPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "세션 저장", "sessions")
	settings, hasSecret, err := s.sessions.GetSettings(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = sessionView{
		Backend: string(settings.Backend), FileDir: settings.FileDir,
		S3Endpoint: settings.S3Endpoint, S3Bucket: settings.S3Bucket, S3Region: settings.S3Region,
		S3AccessKey: settings.S3AccessKey, S3UseSSL: settings.S3UseSSL, HasSecret: hasSecret,
		DefaultMaxSessions: settings.DefaultMaxSessions,
	}
	s.render(w, "sessions", pd)
}

func (s *Server) sessionsUpdate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	maxSessions, _ := strconv.Atoi(r.FormValue("default_max_sessions"))
	err := s.sessions.UpdateSettings(r.Context(), domainproject.UpdateSettingsCommand{
		ActorID: actorID(r),
		Settings: domainproject.Settings{
			Backend:            domainproject.Backend(r.FormValue("backend")),
			FileDir:            r.FormValue("file_dir"),
			S3Endpoint:         r.FormValue("s3_endpoint"),
			S3Bucket:           r.FormValue("s3_bucket"),
			S3Region:           r.FormValue("s3_region"),
			S3AccessKey:        r.FormValue("s3_access_key"),
			S3SecretKey:        r.FormValue("s3_secret_key"),
			S3UseSSL:           r.FormValue("s3_use_ssl") == "on",
			DefaultMaxSessions: maxSessions,
		},
	})
	s.flashResult(w, err, "세션 저장 설정을 저장했습니다.")
	s.redirect(w, r, basePath+"/sessions")
}

func (s *Server) sessionsTest(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.TestConnection(r.Context(), actorID(r)); err != nil {
		s.setFlashError(w, "연결 테스트 실패: "+err.Error())
	} else {
		s.setFlash(w, "연결 테스트에 성공했습니다.")
	}
	s.redirect(w, r, basePath+"/sessions")
}

func (s *Server) memberSetSessionLimit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	maxSessions, _ := strconv.Atoi(r.FormValue("max_sessions"))
	err := s.sessions.SetMemberLimit(r.Context(), actorID(r), r.PathValue("id"), maxSessions)
	s.flashResult(w, err, "최대 세션 수를 변경했습니다.")
	s.redirect(w, r, basePath+"/members")
}

// flashResult 는 use case 결과를 플래시 메시지로 변환한다.
// 실패 시 원시 에러를 사용자에게 노출하지 않고 친화적 메시지로 매핑하며,
// 운영/감사를 위해 원시 에러는 서버 로그로 남긴다(시크릿 누출 방지).
func (s *Server) flashResult(w http.ResponseWriter, err error, okMsg string) {
	if err != nil {
		slog.Warn("admin action failed", "event", "admin.action", "error", err)
		s.setFlashError(w, webErrorMessage(err))
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
			Active: m.Active, CreatedAt: m.CreatedAt.UTC().Format(uiTimeFormat),
			Email: m.Email, DisplayName: m.DisplayName, Organization: m.Organization,
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
			Endpoint: p.Config.Endpoint, APIKeyEnv: p.Config.APIKeyEnv, APIKeySet: p.Config.APIKey != "",
			MaxTokens: p.Config.MaxTokens, Active: p.IsActive,
		})
	}
	return out
}
