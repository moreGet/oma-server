package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainclientversion "aiagent/com/ohmyagent/internal/domain/clientversion"
	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
	domainproject "aiagent/com/ohmyagent/internal/domain/project"
	domainquota "aiagent/com/ohmyagent/internal/domain/quota"
	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// uiTimeFormat 은 어드민 웹 UI 시간 표기 포맷이다(읽기 쉬운 KST, ISO 'T' 미사용).
// JSON API 는 별도로 RFC3339(T 규격, UTC) 를 유지한다 — 여기는 화면 표시 전용.
const uiTimeFormat = "2006-01-02 15:04:05"

const (
	membersPageLimit   = 100 // 멤버 관리 목록 조회 상한(어드민 단일 페이지)
	adminRoomListLimit = 200 // 채팅 관리 방 목록 조회 상한
)

// kstZone 은 한국 표준시(UTC+9, DST 없음)다. tzdata 의존 없이 고정 오프셋으로 표시한다.
var kstZone = time.FixedZone("KST", 9*60*60)

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
	// ToolEditor 는 멤버별 도구 정책 오버라이드 편집기 데이터(모달의 리스트 UI).
	ToolEditor toolEditorView
	// ToolPolicyOverride 는 멤버 도구 오버라이드 존재 여부(모달 배지 표시용).
	ToolPolicyOverride bool
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
	Reasoning string // 추론 강도(reasoning_effort). 빈 값 = 미지정
	APIStyle  string // OpenAI 호출 방식(chat_completions | responses)
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
	ShowChat       bool
	ChatRooms      int
	ChatMessages   int
}

type membersView struct {
	Members []memberView
	Roles   []roleView
	Default domainquota.Limits     // 전역 기본 한도(일·주·월, 0 = 무제한)
	Keys    domainquota.PeriodKeys // 현재 기간 키(표시용)
}

type providersView struct {
	Providers []providerView
	// Reasonings 는 선택 가능한 추론 강도 목록이다(도메인 허용 목록). 페이지 단위로 한 번만 싣는다.
	Reasonings []string
	// APIStyles 는 선택 가능한 OpenAI 호출 방식 목록이다.
	APIStyles []string
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
		// 집계는 DB 에서 GROUP BY 로 끝낸다(쿼리 1회). 예전에는 멤버 행을 전량 힙에 올려
		// 메모리에서 셌는데, 화면에 필요한 건 숫자 4개뿐이라 보유량이 멤버 수에 비례할 이유가 없다.
		if counts, total, err := s.auth.CountMembersByRole(r.Context(), actorID(r)); err == nil {
			dv.MemberTotal = total
			for _, roleID := range []int{domainauth.RoleIDUser, domainauth.RoleIDAdmin, domainauth.RoleIDSuperAdmin} {
				dv.ByRole = append(dv.ByRole, roleCount{Name: domainauth.NameForRoleID(roleID), Count: counts[roleID]})
			}
		}
	}
	if pd.CanManage {
		if providers, err := s.providers.List(r.Context(), actorID(r)); err == nil {
			dv.ProviderTotal = len(providers)
			for _, p := range providers {
				if p.IsActive {
					dv.ActiveProvider = p.Name
					break
				}
			}
		}
		dv.ShowChat = true
		if st, err := s.chat.AdminStats(r.Context()); err == nil {
			dv.ChatRooms = st.Rooms
			dv.ChatMessages = st.Messages
		}
	}
	pd.Data = dv
	s.render(w, "dashboard", pd)
}

// --- 멤버 관리 ---

func (s *Server) membersPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "멤버 관리", "members")
	members, _, err := s.auth.ListMembers(r.Context(), actorID(r), domainauth.MemberFilter{Limit: membersPageLimit})
	if err != nil {
		s.setFlashError(w, "멤버 목록을 볼 권한이 없습니다.")
		s.redirect(w, r, basePath+"/")
		return
	}
	memberViews := toMemberViews(members)
	mv := membersView{Members: memberViews, Roles: toRoleViews(s.controllableRoles(r, pd.User.Level))}

	// 곁다리 조회(쿼터·세션·도구정책)는 모두 이 페이지에 뿌릴 멤버로 범위를 좁힌다.
	// 예전에는 화면에 100명만 뿌리면서 각 테이블을 전량 읽어, 보유량이 표시 대상이 아니라
	// 전체 멤버 수에 비례했다.
	ids := memberIDs(memberViews)
	s.fillQuotaColumns(r, &mv, ids)
	s.fillSessionLimits(r, memberViews, ids)
	s.fillToolPolicyColumns(memberViews, ids)

	pd.Data = mv
	s.render(w, "members", pd)
}

// controllableRoles 는 역할 드롭다운에 노출할 역할만 추린다(actor 보다 낮은 레벨).
// 인가 규칙 CanControl(actor>target)과 UI 를 맞춰, 할당 불가 역할을 골랐다가
// permission denied 로 실패하는 일을 막는다.
func (s *Server) controllableRoles(r *http.Request, actorLevel int) []domainauth.Role {
	roles, _ := s.auth.ListRoles(r.Context())
	out := make([]domainauth.Role, 0, len(roles))
	for _, role := range roles {
		if int(role.Level) < actorLevel {
			out = append(out, role)
		}
	}
	return out
}

// fillQuotaColumns 는 전역 기본값 + 이번 일·주·월 멤버별 한도/사용량을 뷰에 채운다.
// 조회 실패는 페이지를 막지 않고 쿼터 칸만 비운다(멤버 목록 자체는 계속 보여준다).
func (s *Server) fillQuotaColumns(r *http.Request, mv *membersView, ids []string) {
	if s.quota == nil {
		return
	}
	snap, err := s.quota.SnapshotFor(r.Context(), ids)
	if err != nil {
		return
	}
	mv.Default, mv.Keys = snap.Default, snap.Keys
	for i := range mv.Members {
		ov := snap.Limits[mv.Members[i].ID]
		use := snap.Usage[mv.Members[i].ID]
		mv.Members[i].DailyLimit, mv.Members[i].WeeklyLimit, mv.Members[i].MonthlyLimit = ov.Daily, ov.Weekly, ov.Monthly
		mv.Members[i].DailyUsed, mv.Members[i].WeeklyUsed, mv.Members[i].MonthlyUsed = use.Daily, use.Weekly, use.Monthly
		mv.Members[i].Quota = []memberQuotaView{
			quotaWin("일", ov.Daily, snap.Default.Daily, use.Daily),
			quotaWin("주", ov.Weekly, snap.Default.Weekly, use.Weekly),
			quotaWin("월", ov.Monthly, snap.Default.Monthly, use.Monthly),
		}
	}
}

// fillSessionLimits 는 멤버별 최대 세션 수 오버라이드를 채운다(모달 입력용).
func (s *Server) fillSessionLimits(r *http.Request, views []memberView, ids []string) {
	if s.sessions == nil {
		return
	}
	limits, err := s.sessions.MemberLimitsFor(r.Context(), ids)
	if err != nil {
		return
	}
	for i := range views {
		views[i].SessionLimit = limits[views[i].ID]
	}
}

// fillToolPolicyColumns 는 멤버별 도구 정책 오버라이드를 채운다(모달의 도구 리스트 UI).
// 오버라이드가 없는 멤버는 전부 '기본'으로 표시된다.
func (s *Server) fillToolPolicyColumns(views []memberView, ids []string) {
	policies := map[string]domaintoolpolicy.MemberPolicy{}
	if s.toolPolicy != nil {
		// 이 페이지에 뿌릴 id 만 넘긴다 — 전량 스냅샷을 받으면 화면에 없는 멤버까지 복사된다.
		policies = s.toolPolicy.MemberPoliciesFor(ids)
	}
	for i := range views {
		p := policies[views[i].ID]
		views[i].ToolEditor = buildToolEditor("m"+views[i].ID, p.Enabled, p.Disabled)
		views[i].ToolPolicyOverride = len(p.Enabled) > 0 || len(p.Disabled) > 0
	}
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
	s.flashRedirect(w, r, err, "멤버를 생성했습니다.", "/members")
}

func (s *Server) memberChangeRole(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	roleID, _ := strconv.Atoi(r.FormValue("role_id"))
	_, err := s.auth.ChangeRole(r.Context(), domainauth.ChangeRoleCommand{
		ActorID:  actorID(r),
		TargetID: r.PathValue("id"),
		RoleID:   roleID,
	})
	s.flashRedirect(w, r, err, "역할을 변경했습니다.", "/members")
}

func (s *Server) memberToggleActive(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	active := r.FormValue("active") == "true"
	_, err := s.auth.SetActive(r.Context(), domainauth.SetActiveCommand{
		ActorID:  actorID(r),
		TargetID: r.PathValue("id"),
		Active:   active,
	})
	s.flashRedirect(w, r, err, "활성 상태를 변경했습니다.", "/members")
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
	s.flashRedirect(w, r, err, "프로필을 변경했습니다.", "/members")
}

func (s *Server) memberResetPassword(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.auth.ResetPassword(r.Context(), actorID(r), r.PathValue("id"), r.FormValue("new_password"))
	s.flashRedirect(w, r, err, "비밀번호를 초기화했습니다.", "/members")
}

func (s *Server) memberDelete(w http.ResponseWriter, r *http.Request) {
	err := s.auth.DeleteMember(r.Context(), actorID(r), r.PathValue("id"))
	s.flashRedirect(w, r, err, "멤버를 삭제했습니다.", "/members")
}

func (s *Server) memberSetTokenLimit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.quota.SetMemberLimits(r.Context(), actorID(r), r.PathValue("id"), formLimits(r))
	s.flashRedirect(w, r, err, "토큰 한도를 변경했습니다.", "/members")
}

func (s *Server) quotaSetDefault(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.quota.SetDefaultLimits(r.Context(), actorID(r), formLimits(r))
	s.flashRedirect(w, r, err, "전역 기본 토큰 한도를 변경했습니다.", "/members")
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
	s.flashRedirect(w, r, err, "사용량을 초기화했습니다.", "/members")
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
	if !s.requireManage(w, r) {
		return
	}
	pd := s.base(r, w, "LLM Provider", "providers")
	providers, err := s.providers.List(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, "Provider 목록을 불러오지 못했습니다.")
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = providersView{
		Providers:  toProviderViews(providers),
		Reasonings: domainllmprovider.ReasoningEfforts,
		APIStyles:  domainllmprovider.APIStyles,
	}
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
			Reasoning: r.FormValue("reasoning"),
			APIStyle:  r.FormValue("api_style"),
		},
		ActorID: actorID(r),
	})
	s.flashRedirect(w, r, err, "Provider를 등록했습니다.", "/providers")
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
			Reasoning: r.FormValue("reasoning"),
			APIStyle:  r.FormValue("api_style"),
		},
		ActorID: actorID(r),
	})
	s.flashRedirect(w, r, err, "Provider 설정을 저장했습니다.", "/providers")
}

func (s *Server) providerActivate(w http.ResponseWriter, r *http.Request) {
	err := s.providers.Activate(r.Context(), domainllmprovider.ActivateCommand{ID: r.PathValue("id"), ActorID: actorID(r)})
	s.flashRedirect(w, r, err, "활성 Provider를 변경했습니다.", "/providers")
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
	s.flashRedirect(w, r, err, "Provider를 삭제했습니다.", "/providers")
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
	s.flashRedirect(w, r, err, "대화 이력 설정을 저장했습니다.", "/transcripts")
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
	s.flashRedirect(w, r, err, "세션 저장 설정을 저장했습니다.", "/sessions")
}

func (s *Server) sessionsTest(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.TestConnection(r.Context(), actorID(r)); err != nil {
		s.setFlashError(w, "연결 테스트 실패: "+err.Error())
	} else {
		s.setFlash(w, "연결 테스트에 성공했습니다.")
	}
	s.redirect(w, r, basePath+"/sessions")
}

// --- 클라이언트 버전(/admin/client) ---

type clientVersionView struct {
	Latest           string
	MinimumSupported string
	DownloadURL      string
	Notice           string
	Mandatory        bool
	UpdatedAt        string
	UpdatedBy        string
}

func (s *Server) clientPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "클라이언트 버전", "client")
	st, err := s.clientVersion.GetSettings(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = clientVersionView{
		Latest: st.Latest, MinimumSupported: st.MinimumSupported, DownloadURL: st.DownloadURL,
		Notice: st.Notice, Mandatory: st.Mandatory, UpdatedAt: fmtUnixKST(st.UpdatedAt), UpdatedBy: st.UpdatedBy,
	}
	s.render(w, "client", pd)
}

func (s *Server) clientUpdate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	err := s.clientVersion.UpdateSettings(r.Context(), domainclientversion.UpdateCommand{
		ActorID: actorID(r),
		Settings: domainclientversion.Settings{
			Latest:           r.FormValue("latest"),
			MinimumSupported: r.FormValue("minimum_supported"),
			DownloadURL:      r.FormValue("download_url"),
			Notice:           r.FormValue("notice"),
			Mandatory:        r.FormValue("mandatory") == "on",
		},
	})
	s.flashRedirect(w, r, err, "클라이언트 버전 설정을 저장했습니다.", "/client")
}

// --- 에이전트 레지스트리(/admin/agents) ---

// agentRow 는 어드민 에이전트 목록 한 행의 표시 데이터다.
type agentRow struct {
	ID            string
	Name          string
	Status        string // online | stale | offline (뱃지 색 분기)
	Endpoint      string
	Capabilities  string // 콤마 연결 표시
	Tags          string
	Model         string
	Version       string
	LastHeartbeat string // KST 표시("" = heartbeat 기록 없음)
	Owner         string // username(해석 실패 시 member id 폴백)
}

type agentsView struct {
	Agents  []agentRow
	Online  int
	Stale   int
	Offline int
}

func (s *Server) agentsPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "에이전트", "agents")
	list, err := s.agents.AdminList(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	v := agentsView{Agents: make([]agentRow, 0, len(list))}
	for _, a := range list {
		owner := a.OwnerName
		if owner == "" {
			owner = a.OwnerID // 삭제된 멤버 등 이름 해석 실패 시 id 폴백
		}
		switch a.Status {
		case domainagentregistry.StatusOnline:
			v.Online++
		case domainagentregistry.StatusStale:
			v.Stale++
		default:
			v.Offline++
		}
		v.Agents = append(v.Agents, agentRow{
			ID:            a.ID,
			Name:          a.Name,
			Status:        string(a.Status),
			Endpoint:      a.EndpointURL,
			Capabilities:  strings.Join(a.Capabilities, ", "),
			Tags:          strings.Join(a.Tags, ", "),
			Model:         a.Model,
			Version:       a.Version,
			LastHeartbeat: fmtUnixKST(a.LastHeartbeatAt.Unix()),
			Owner:         owner,
		})
	}
	pd.Data = v
	s.render(w, "agents", pd)
}

// agentDelete 는 어드민 강제 해제다(소유자 무관 삭제 — 좀비/고아 레코드 정리).
func (s *Server) agentDelete(w http.ResponseWriter, r *http.Request) {
	err := s.agents.AdminDelete(r.Context(), actorID(r), r.PathValue("id"))
	s.flashRedirect(w, r, err, "에이전트 등록을 해제했습니다.", "/agents")
}

// --- 채팅 관리(/admin/chat) ---

type chatStatsView struct {
	Rooms, GroupRooms, DirectRooms         int
	Messages, DeletedMessages, Attachments int
	AttachmentSize                         string
}

type adminRoomRow struct {
	ID, Type, Name            string
	MemberCount, MessageCount int
	LastActivity              string
}

type chatView struct {
	Stats chatStatsView
	Rooms []adminRoomRow
}

type adminMsgRow struct {
	ID, SenderID, Content, CreatedAt string
	Deleted                          bool
	Attachments                      int
}

type chatRoomView struct {
	ID, Type, Name string
	Members        []string
	Messages       []adminMsgRow
}

// requireManage 는 admin↑ 가 아니면 플래시 후 대시보드로 보내고 false 를 반환한다(어드민 게이트).
func (s *Server) requireManage(w http.ResponseWriter, r *http.Request) bool {
	if c, ok := security.ClaimsFrom(r.Context()); ok && c.Level >= domainauth.RoleLevelAdmin {
		return true
	}
	s.setFlashError(w, "권한이 없습니다.")
	s.redirect(w, r, basePath+"/")
	return false
}

func roomLabel(typ, name string) string {
	if typ == "direct" {
		return "1:1 대화"
	}
	if strings.TrimSpace(name) == "" {
		return "(이름 없음)"
	}
	return name
}

func (s *Server) chatPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "채팅 관리", "chat")
	if !s.requireManage(w, r) {
		return
	}
	stats, err := s.chat.AdminStats(r.Context())
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	rooms, _ := s.chat.AdminListRooms(r.Context(), adminRoomListLimit)
	cv := chatView{Stats: chatStatsView{
		Rooms: stats.Rooms, GroupRooms: stats.GroupRooms, DirectRooms: stats.DirectRooms,
		Messages: stats.Messages, DeletedMessages: stats.DeletedMessages,
		Attachments: stats.Attachments, AttachmentSize: humanBytes(stats.AttachmentBytes),
	}}
	cv.Rooms = make([]adminRoomRow, 0, len(rooms))
	for _, rm := range rooms {
		cv.Rooms = append(cv.Rooms, adminRoomRow{
			ID: rm.ID, Type: string(rm.Type), Name: roomLabel(string(rm.Type), rm.Name),
			MemberCount: rm.MemberCount, MessageCount: rm.MessageCount, LastActivity: fmtUnixKST(rm.LastActivity),
		})
	}
	pd.Data = cv
	s.render(w, "chat", pd)
}

func (s *Server) chatRoomPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "채팅 방", "chat")
	if !s.requireManage(w, r) {
		return
	}
	room, members, msgs, err := s.chat.AdminRoomDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/chat")
		return
	}
	rv := chatRoomView{ID: room.ID, Type: string(room.Type), Name: roomLabel(string(room.Type), room.Name), Members: members}
	rv.Messages = make([]adminMsgRow, 0, len(msgs))
	for _, m := range msgs {
		rv.Messages = append(rv.Messages, adminMsgRow{
			ID: m.ID, SenderID: m.SenderID, Content: m.Content, CreatedAt: fmtUnixKST(m.CreatedAt),
			Deleted: m.DeletedAt > 0, Attachments: len(m.Attachments),
		})
	}
	pd.Data = rv
	s.render(w, "chat_room", pd)
}

func (s *Server) chatRoomDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	err := s.chat.AdminDeleteRoom(r.Context(), r.PathValue("id"))
	s.flashRedirect(w, r, err, "방을 삭제했습니다.", "/chat")
}

func (s *Server) chatMessageDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	_ = r.ParseForm()
	roomID := r.FormValue("room_id")
	err := s.chat.AdminDeleteMessage(r.Context(), r.PathValue("id"))
	s.flashResult(w, err, "메시지를 삭제했습니다.")
	if roomID != "" {
		s.redirect(w, r, basePath+"/chat/rooms/"+roomID)
		return
	}
	s.redirect(w, r, basePath+"/chat")
}

// humanBytes 는 바이트 수를 사람이 읽는 단위로 포맷한다.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// --- 도구 정책(/admin/tools) ---

// toolChipView 는 카탈로그 도구 1개의 상태다(State: default|enabled|disabled).
type toolChipView struct {
	Name  string
	State string
}

// toolCategoryView 는 카테고리별 도구 묶음이다(노출 순서 보존).
type toolCategoryView struct {
	Category string
	Tools    []toolChipView
}

// toolEditorView 는 도구 허용/차단 편집기(카테고리 리스트 + 카탈로그 외 수동 입력) 공용 데이터다.
// 전역 도구 정책 페이지와 멤버 모달이 같은 템플릿 파셜("toolEditor")로 렌더한다.
type toolEditorView struct {
	IDPrefix        string             // 라디오 id 유일화 접두사(멤버별 모달이 한 페이지에 여러 개라 필수)
	Categories      []toolCategoryView // 카탈로그 도구(카테고리별)
	UnknownEnabled  string             // 카탈로그 외 허용 도구(줄바꿈)
	UnknownDisabled string             // 카탈로그 외 차단 도구(줄바꿈)
}

// toolPolicyView 는 전역 도구 정책 편집 화면 데이터다.
type toolPolicyView struct {
	Mode           string
	toolEditorView        // 임베드: Categories/UnknownEnabled/UnknownDisabled 승격
	PatternsJSON   string // [{type,pattern,reason,script_type}]
	PathsJSON      string // [{type,pattern,reason}]
	UpdatedAt      string // KST, 비어있으면 미저장
	UpdatedBy      string
}

func (s *Server) toolsPage(w http.ResponseWriter, r *http.Request) {
	pd := s.base(r, w, "도구 정책", "tools")
	st, err := s.toolPolicy.GetSettings(r.Context(), actorID(r))
	if err != nil {
		s.setFlashError(w, webErrorMessage(err))
		s.redirect(w, r, basePath+"/")
		return
	}
	pd.Data = toolPolicyView{
		Mode:           st.Mode,
		toolEditorView: buildToolEditor("g", st.Enabled, st.Disabled),
		PatternsJSON:   marshalIndent(st.BlockedPatterns),
		PathsJSON:      marshalIndent(st.BlockedPaths),
		UpdatedAt:      fmtUnixKST(st.UpdatedAt),
		UpdatedBy:      st.UpdatedBy,
	}
	s.render(w, "tools", pd)
}

// buildToolEditor 는 허용/차단 목록을 카테고리 리스트 편집기 데이터로 변환한다(idPrefix 는 라디오 id 충돌 방지: 전역="g", 멤버="m"+ID).
// 도구 상태는 차단 우선(authorize 와 동일): disabled > enabled > default.
func buildToolEditor(idPrefix string, enabled, disabled []string) toolEditorView {
	enabledSet := toStringSet(enabled)
	disabledSet := toStringSet(disabled)

	var cats []toolCategoryView
	idx := make(map[string]int)
	for _, t := range domaintoolpolicy.ClientTools {
		state := "default"
		if _, ok := disabledSet[t.Name]; ok {
			state = "disabled"
		} else if _, ok := enabledSet[t.Name]; ok {
			state = "enabled"
		}
		i, ok := idx[t.Category]
		if !ok {
			i = len(cats)
			idx[t.Category] = i
			cats = append(cats, toolCategoryView{Category: t.Category})
		}
		cats[i].Tools = append(cats[i].Tools, toolChipView{Name: t.Name, State: state})
	}
	return toolEditorView{
		IDPrefix:        idPrefix,
		Categories:      cats,
		UnknownEnabled:  strings.Join(unknownTools(enabled), "\n"),
		UnknownDisabled: strings.Join(unknownTools(disabled), "\n"),
	}
}

// toStringSet 은 슬라이스를 조회용 set 으로 만든다.
func toStringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, v := range in {
		out[v] = struct{}{}
	}
	return out
}

// unknownTools 는 카탈로그에 없는(레거시/커스텀) 도구명만 추린다.
func unknownTools(in []string) []string {
	var out []string
	for _, n := range in {
		if !domaintoolpolicy.IsKnownTool(n) {
			out = append(out, n)
		}
	}
	return out
}

// parseToolEditorForm 은 도구 편집기 폼(t_<name> 라디오 + enabled_extra/disabled_extra)을
// enabled/disabled 슬라이스로 재구성한다(전역·멤버 폼 공용).
func parseToolEditorForm(r *http.Request) (enabled, disabled []string) {
	for _, t := range domaintoolpolicy.ClientTools {
		switch r.FormValue("t_" + t.Name) {
		case "enabled":
			enabled = append(enabled, t.Name)
		case "disabled":
			disabled = append(disabled, t.Name)
		}
	}
	// 카탈로그 외 도구는 수동 입력으로 보존.
	enabled = append(enabled, splitLines(r.FormValue("enabled_extra"))...)
	disabled = append(disabled, splitLines(r.FormValue("disabled_extra"))...)
	return enabled, disabled
}

func (s *Server) toolsUpdate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	patterns, perr := parseBlockedPatterns(r.FormValue("blocked_patterns"))
	paths, qerr := parseBlockedPaths(r.FormValue("blocked_paths"))
	if perr != nil || qerr != nil {
		s.setFlashError(w, "차단 패턴/경로 JSON 형식 오류 — 입력을 확인하세요.")
		s.redirect(w, r, basePath+"/tools")
		return
	}

	enabled, disabled := parseToolEditorForm(r)

	err := s.toolPolicy.UpdateSettings(r.Context(), domaintoolpolicy.UpdateCommand{
		ActorID: actorID(r),
		Settings: domaintoolpolicy.Settings{
			Mode:            r.FormValue("mode"),
			Enabled:         enabled,
			Disabled:        disabled,
			BlockedPatterns: patterns,
			BlockedPaths:    paths,
		},
	})
	s.flashRedirect(w, r, err, "도구 정책을 저장했습니다.", "/tools")
}

// splitLines 는 줄바꿈 구분 텍스트를 trim·빈 줄 제거한 슬라이스로 만든다.
func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(ln); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseBlockedPatterns 는 JSON 텍스트(빈 값=없음)를 패턴 슬라이스로 파싱한다.
func parseBlockedPatterns(s string) ([]domaintoolpolicy.BlockedPattern, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []domaintoolpolicy.BlockedPattern
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseBlockedPaths 는 JSON 텍스트(빈 값=없음)를 경로 슬라이스로 파싱한다.
func parseBlockedPaths(s string) ([]domaintoolpolicy.BlockedPath, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []domaintoolpolicy.BlockedPath
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalIndent 는 슬라이스를 보기 좋은 JSON 텍스트로 직렬화한다(빈 슬라이스는 빈 문자열).
func marshalIndent(v any) string {
	switch t := v.(type) {
	case []domaintoolpolicy.BlockedPattern:
		if len(t) == 0 {
			return ""
		}
	case []domaintoolpolicy.BlockedPath:
		if len(t) == 0 {
			return ""
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// fmtUnixKST 는 unix 초를 KST 표시 문자열로 변환한다(0이면 빈 문자열).
func fmtUnixKST(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).In(kstZone).Format(uiTimeFormat)
}

func (s *Server) memberSetSessionLimit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	maxSessions, _ := strconv.Atoi(r.FormValue("max_sessions"))
	err := s.sessions.SetMemberLimit(r.Context(), actorID(r), r.PathValue("id"), maxSessions)
	s.flashRedirect(w, r, err, "최대 세션 수를 변경했습니다.", "/members")
}

// memberSetToolPolicy 는 멤버별 도구 정책 오버라이드(허용/차단)를 저장한다(빈 입력=오버라이드 해제).
func (s *Server) memberSetToolPolicy(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	enabled, disabled := parseToolEditorForm(r)
	err := s.toolPolicy.UpdateMemberPolicy(r.Context(), domaintoolpolicy.MemberUpdateCommand{
		MemberID: r.PathValue("id"),
		Enabled:  enabled,
		Disabled: disabled,
		ActorID:  actorID(r),
	})
	s.flashRedirect(w, r, err, "멤버 도구 정책을 저장했습니다.", "/members")
}

// flashRedirect 는 어드민 폼 POST 의 공통 마무리다: 결과 플래시 후 목록 페이지로 리다이렉트.
func (s *Server) flashRedirect(w http.ResponseWriter, r *http.Request, err error, okMsg, path string) {
	s.flashResult(w, err, okMsg)
	s.redirect(w, r, basePath+path)
}

// flashResult 는 use case 결과를 플래시 메시지로 변환한다(실패 시 친화 메시지로 매핑, 시크릿 누출 방지).
// 원시 에러는 운영/감사를 위해 서버 로그로만 남긴다.
func (s *Server) flashResult(w http.ResponseWriter, err error, okMsg string) {
	if err != nil {
		slog.Warn("admin action failed", "event", "admin.action", "error", err)
		s.setFlashError(w, webErrorMessage(err))
		return
	}
	s.setFlash(w, okMsg)
}

// webErrorMessage 는 도메인 에러를 사용자 친화 한글 메시지로 매핑한다(글로벌 예외 처리).
// 검증 에러는 그대로 노출하고, 알 수 없는 에러는 내부 상세를 숨겨 일반 메시지로 대체한다.
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
	case errors.Is(err, domainauth.ErrNotFound), errors.Is(err, domainllmprovider.ErrNotFound), errors.Is(err, domainagentregistry.ErrNotFound):
		return "대상을 찾을 수 없습니다."
	default:
		return "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해주세요."
	}
}

// --- 매핑 ---

// memberIDs 는 화면에 렌더할 멤버들의 id 만 추린다(곁다리 조회를 이 페이지 범위로 좁히는 데 쓴다).
func memberIDs(vs []memberView) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func toMemberViews(ms []domainauth.Member) []memberView {
	out := make([]memberView, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberView{
			ID: m.ID, Username: m.Username, Role: m.Role.Name, Level: int(m.Role.Level),
			Active: m.Active, CreatedAt: m.CreatedAt.In(kstZone).Format(uiTimeFormat),
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
			MaxTokens: p.Config.MaxTokens, Reasoning: p.Config.Reasoning,
			APIStyle: p.Config.APIStyle, Active: p.IsActive,
		})
	}
	return out
}
