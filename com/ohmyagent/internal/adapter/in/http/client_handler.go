package httpin

import (
	"encoding/json"
	"net/http"

	domaintoolpolicy "aiagent/com/ohmyagent/internal/domain/toolpolicy"
)

// PolicyProvider 는 현재 도구/명령 정책 스냅샷을 제공한다(DB 백엔드 매니저가 atomic 캐시로 구현).
// 핸들러는 요청마다 DB 를 치지 않고 이 무락 스냅샷을 읽는다.
type PolicyProvider interface {
	// EffectivePolicy 는 멤버에 적용되는 유효 도구 정책(전역 ⊕ 멤버 오버라이드)을 반환한다.
	EffectivePolicy(memberID string) (mode string, enabled, disabled []string)
	CommandPolicy() (patterns []domaintoolpolicy.BlockedPattern, paths []domaintoolpolicy.BlockedPath)
}

// VersionProvider 는 현재 클라이언트 버전 정보를 제공한다(DB 백엔드 매니저가 atomic 캐시로 구현).
type VersionProvider interface {
	ClientVersion() (latest, minimumSupported, downloadURL, notice string, mandatory bool)
}

// ClientHandler 는 클라이언트 계약(도구 정책 / 버전 점검 / 명령 보안) 핸들러다(중첩 envelope).
// 도구·명령 정책과 버전 정보 모두 DB 백엔드(PolicyProvider/VersionProvider)에서 온다.
type ClientHandler struct {
	version VersionProvider
	policy  PolicyProvider
}

// NewClientHandler 는 ClientHandler 를 생성한다.
func NewClientHandler(version VersionProvider, policy PolicyProvider) *ClientHandler {
	return &ClientHandler{version: version, policy: policy}
}

// --- GET /api/v1/tools/policy (로그인 시 1회) ---

type toolPolicyResp struct {
	Mode     string   `json:"mode"`     // cached | realtime
	Enabled  []string `json:"enabled"`  // null = 전체 허용
	Disabled []string `json:"disabled"` // 블랙리스트(enabled 보다 우선)
}

// ToolsPolicy 는 세션 도구 정책(모드 + cached 목록)을 반환한다(인증 멤버에 유효한 정책).
func (h *ClientHandler) ToolsPolicy(w http.ResponseWriter, r *http.Request) error {
	mode, enabled, disabled := h.policy.EffectivePolicy(actorID(r))
	if mode != "realtime" {
		mode = "cached" // 그 외 값은 cached 로 간주(스펙)
	}
	writeJSON(w, http.StatusOK, toolPolicyResp{Mode: mode, Enabled: enabled, Disabled: disabled})
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
	if err := decodeJSON(w, r, maxJSONBytes, &req); err != nil {
		return err
	}
	if req.Tool == "" {
		return ErrBadRequest("tool is required")
	}
	allowed, reason := h.authorize(actorID(r), req.Tool)
	writeJSON(w, http.StatusOK, toolAuthorizeResp{Allowed: allowed, Reason: reason})
	return nil
}

// authorize 는 도메인 판정(단일 진실)에 위임한다.
// 같은 규칙을 /agent/chat 의 요청 게이트도 쓴다 — 여기에 로직을 복제하면 둘이 갈라진다.
func (h *ClientHandler) authorize(memberID, tool string) (bool, *string) {
	_, enabled, disabled := h.policy.EffectivePolicy(memberID)
	allowed, reason := domaintoolpolicy.Authorize(enabled, disabled, tool)
	if allowed {
		return true, nil
	}
	return false, &reason
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
	latest, minimum, url, notice, mandatory := h.version.ClientVersion()
	writeJSON(w, http.StatusOK, clientVersionResp{
		Latest:           latest,
		MinimumSupported: minimum,
		DownloadURL:      url,
		Notice:           notice,
		Mandatory:        mandatory,
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
// 값은 도메인에서 정규화·빈 항목 제거되어 캐시되므로 그대로 매핑한다.
func (h *ClientHandler) CommandPolicy(w http.ResponseWriter, r *http.Request) error {
	patterns, paths := h.policy.CommandPolicy()
	out := commandPolicyResp{BlockedPatterns: []commandPatternResp{}, BlockedPaths: []commandPathResp{}}
	for _, p := range patterns {
		out.BlockedPatterns = append(out.BlockedPatterns, commandPatternResp{
			Type: p.Type, Pattern: p.Pattern, Reason: p.Reason, ScriptType: p.ScriptType,
		})
	}
	for _, p := range paths {
		out.BlockedPaths = append(out.BlockedPaths, commandPathResp{
			Type: p.Type, Pattern: p.Pattern, Reason: p.Reason,
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
