package httpin

import (
	"encoding/json"
	"net/http"
)

// ToolPolicyConfig 는 도구 정책 핸들러 주입값이다(config 에서 매핑).
type ToolPolicyConfig struct {
	Mode     string   // cached | realtime
	Enabled  []string // nil = 전체 허용
	Disabled []string
}

// ClientVersionConfig 는 버전 점검 핸들러 주입값이다(config 에서 매핑).
type ClientVersionConfig struct {
	Latest           string
	MinimumSupported string
	DownloadURL      string
	Notice           string
	Mandatory        bool
}

// CommandPolicyConfig 는 위험명령/경로 차단 정책 핸들러 주입값이다(config 에서 매핑).
type CommandPolicyConfig struct {
	BlockedPatterns []CommandBlockedPattern
	BlockedPaths    []CommandBlockedPath
}

// CommandBlockedPattern 은 차단 명령 패턴 1건이다.
type CommandBlockedPattern struct {
	Type       string // regex | substring
	Pattern    string
	Reason     string
	ScriptType string // any | powershell | cmd
}

// CommandBlockedPath 은 차단 경로 패턴 1건이다.
type CommandBlockedPath struct {
	Type    string // regex | substring
	Pattern string
	Reason  string
}

// ClientHandler 는 클라이언트 계약(도구 정책 / 버전 점검 / 명령 보안) 핸들러다(중첩 envelope).
type ClientHandler struct {
	policy    ToolPolicyConfig
	version   ClientVersionConfig
	cmdPolicy CommandPolicyConfig
}

// NewClientHandler 는 ClientHandler 를 생성한다.
func NewClientHandler(policy ToolPolicyConfig, version ClientVersionConfig, cmdPolicy CommandPolicyConfig) *ClientHandler {
	return &ClientHandler{policy: policy, version: version, cmdPolicy: cmdPolicy}
}

// --- GET /api/v1/tools/policy (로그인 시 1회) ---

type toolPolicyResp struct {
	Mode     string   `json:"mode"`     // cached | realtime
	Enabled  []string `json:"enabled"`  // null = 전체 허용
	Disabled []string `json:"disabled"` // 블랙리스트(enabled 보다 우선)
}

// ToolsPolicy 는 세션 도구 정책(모드 + cached 목록)을 반환한다.
func (h *ClientHandler) ToolsPolicy(w http.ResponseWriter, r *http.Request) error {
	mode := h.policy.Mode
	if mode != "realtime" {
		mode = "cached" // 그 외 값은 cached 로 간주(스펙)
	}
	writeJSON(w, http.StatusOK, toolPolicyResp{Mode: mode, Enabled: h.policy.Enabled, Disabled: h.policy.Disabled})
	return nil
}

// --- POST /api/v1/tools/authorize (realtime 모드, 도구 실행 직전) ---

type toolAuthorizeReq struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolAuthorizeResp struct {
	Allowed bool    `json:"allowed"`
	Reason  *string `json:"reason"`
}

// ToolsAuthorize 는 특정 도구 1회 실행을 인가한다(disabled 우선, enabled 화이트리스트).
func (h *ClientHandler) ToolsAuthorize(w http.ResponseWriter, r *http.Request) error {
	defer func() { _ = r.Body.Close() }()
	var req toolAuthorizeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ErrBadRequest("invalid request body")
	}
	if req.Tool == "" {
		return ErrBadRequest("tool is required")
	}
	allowed, reason := h.authorize(req.Tool)
	writeJSON(w, http.StatusOK, toolAuthorizeResp{Allowed: allowed, Reason: reason})
	return nil
}

func (h *ClientHandler) authorize(tool string) (bool, *string) {
	for _, d := range h.policy.Disabled {
		if d == tool {
			reason := "서버 정책에 의해 차단된 도구입니다"
			return false, &reason
		}
	}
	if len(h.policy.Enabled) > 0 {
		for _, e := range h.policy.Enabled {
			if e == tool {
				return true, nil
			}
		}
		reason := "허용 목록에 없는 도구입니다"
		return false, &reason
	}
	return true, nil
}

// --- GET /api/v1/client/version (연결·인증 직후 1회) ---

type clientVersionResp struct {
	Latest           string `json:"latest"`
	MinimumSupported string `json:"minimum_supported"`
	DownloadURL      string `json:"download_url,omitempty"`
	Notice           string `json:"notice,omitempty"`
	Mandatory        bool   `json:"mandatory"`
}

// ClientVersion 은 최신/최소지원 클라 버전 정보를 반환한다.
func (h *ClientHandler) ClientVersion(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, clientVersionResp{
		Latest:           h.version.Latest,
		MinimumSupported: h.version.MinimumSupported,
		DownloadURL:      h.version.DownloadURL,
		Notice:           h.version.Notice,
		Mandatory:        h.version.Mandatory,
	})
	return nil
}

// --- GET /api/v1/security/command-policy (로그인 시 1회) ---

type commandPatternResp struct {
	Type       string `json:"type"`             // regex | substring
	Pattern    string `json:"pattern"`          //
	Reason     string `json:"reason,omitempty"` // 차단 사유(없으면 생략)
	ScriptType string `json:"script_type"`      // any | powershell | cmd
}

type commandPathResp struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
	Reason  string `json:"reason,omitempty"`
}

type commandPolicyResp struct {
	BlockedPatterns []commandPatternResp `json:"blocked_patterns"`
	BlockedPaths    []commandPathResp    `json:"blocked_paths"`
}

// CommandPolicy 는 서버 추가 위험명령/경로 차단 패턴을 반환한다("2중 안전": 추가만, 디폴트 해제 불가).
// 빈 패턴은 건너뛰고, type/script_type 은 안전 기본값으로 정규화한다(생략=substring/any).
func (h *ClientHandler) CommandPolicy(w http.ResponseWriter, r *http.Request) error {
	out := commandPolicyResp{BlockedPatterns: []commandPatternResp{}, BlockedPaths: []commandPathResp{}}
	for _, p := range h.cmdPolicy.BlockedPatterns {
		if p.Pattern == "" {
			continue
		}
		out.BlockedPatterns = append(out.BlockedPatterns, commandPatternResp{
			Type: normMatchType(p.Type), Pattern: p.Pattern, Reason: p.Reason, ScriptType: normScriptType(p.ScriptType),
		})
	}
	for _, p := range h.cmdPolicy.BlockedPaths {
		if p.Pattern == "" {
			continue
		}
		out.BlockedPaths = append(out.BlockedPaths, commandPathResp{
			Type: normMatchType(p.Type), Pattern: p.Pattern, Reason: p.Reason,
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// normMatchType 은 매칭 방식을 정규화한다(regex 만 그대로, 그 외=substring 안전 기본).
func normMatchType(t string) string {
	if t == "regex" {
		return "regex"
	}
	return "substring"
}

// normScriptType 은 적용 셸을 정규화한다(powershell|cmd 만 그대로, 그 외=any).
func normScriptType(s string) string {
	switch s {
	case "powershell", "cmd":
		return s
	default:
		return "any"
	}
}
