package httpin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientHandler_ToolsPolicy(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{Mode: "", Disabled: []string{"kill_process"}}, ClientVersionConfig{}, CommandPolicyConfig{})
	rec := httptest.NewRecorder()
	require.NoError(t, h.ToolsPolicy(rec, httptest.NewRequest("GET", "/api/v1/tools/policy", nil)))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "cached", resp["mode"]) // 빈/미지정 → cached
	assert.Nil(t, resp["enabled"])          // nil → null = 전체 허용
}

func TestClientHandler_Authorize(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{Enabled: []string{"read_file"}, Disabled: []string{"kill_process"}}, ClientVersionConfig{}, CommandPolicyConfig{})

	a, reason := h.authorize("kill_process") // disabled 우선
	assert.False(t, a)
	assert.NotNil(t, reason)

	a, _ = h.authorize("read_file") // enabled 화이트리스트 포함
	assert.True(t, a)

	a, _ = h.authorize("write_file") // enabled 지정인데 미포함 → 차단
	assert.False(t, a)

	open := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{}, CommandPolicyConfig{}) // 목록 없음 → 전체 허용
	a, _ = open.authorize("anything")
	assert.True(t, a)
}

func TestClientHandler_AuthorizeEndpoint(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{Disabled: []string{"kill_process"}}, ClientVersionConfig{}, CommandPolicyConfig{})
	rec := httptest.NewRecorder()
	require.NoError(t, h.ToolsAuthorize(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"tool":"kill_process"}`))))

	var resp struct {
		Allowed bool    `json:"allowed"`
		Reason  *string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Reason)
}

func TestClientHandler_CommandPolicy(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{}, CommandPolicyConfig{
		BlockedPatterns: []CommandBlockedPattern{
			{Type: "regex", Pattern: `\bnet\s+user\b`, Reason: "계정 조작 금지", ScriptType: "powershell"},
			{Type: "", Pattern: "bcdedit"}, // type/script_type 생략 → 정규화
			{Type: "regex", Pattern: ""},   // 빈 패턴 → skip
		},
		BlockedPaths: []CommandBlockedPath{
			{Pattern: `D:\sensitive`, Reason: "민감 경로"},
			{Pattern: ""}, // skip
		},
	})
	rec := httptest.NewRecorder()
	require.NoError(t, h.CommandPolicy(rec, httptest.NewRequest("GET", "/api/v1/security/command-policy", nil)))

	var resp struct {
		BlockedPatterns []map[string]any `json:"blocked_patterns"`
		BlockedPaths    []map[string]any `json:"blocked_paths"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	require.Len(t, resp.BlockedPatterns, 2) // 빈 패턴 제외
	assert.Equal(t, "regex", resp.BlockedPatterns[0]["type"])
	assert.Equal(t, "powershell", resp.BlockedPatterns[0]["script_type"])
	assert.Equal(t, "substring", resp.BlockedPatterns[1]["type"])  // 생략 → substring
	assert.Equal(t, "any", resp.BlockedPatterns[1]["script_type"]) // 생략 → any
	_, hasReason := resp.BlockedPatterns[1]["reason"]              // reason 빈 값 → omitempty
	assert.False(t, hasReason)

	require.Len(t, resp.BlockedPaths, 1) // 빈 패턴 제외
	assert.Equal(t, "substring", resp.BlockedPaths[0]["type"])
	assert.Equal(t, `D:\sensitive`, resp.BlockedPaths[0]["pattern"])
}

func TestClientHandler_CommandPolicy_EmptyReturnsArrays(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{}, CommandPolicyConfig{})
	rec := httptest.NewRecorder()
	require.NoError(t, h.CommandPolicy(rec, httptest.NewRequest("GET", "/x", nil)))
	// 미설정이어도 null 이 아니라 빈 배열로 응답(클라 파싱 단순화).
	assert.JSONEq(t, `{"blocked_patterns":[],"blocked_paths":[]}`, rec.Body.String())
}

func TestClientHandler_Version(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{Latest: "1.4.0", MinimumSupported: "1.2.0", Mandatory: true}, CommandPolicyConfig{})
	rec := httptest.NewRecorder()
	require.NoError(t, h.ClientVersion(rec, httptest.NewRequest("GET", "/x", nil)))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "1.4.0", resp["latest"])
	assert.Equal(t, "1.2.0", resp["minimum_supported"])
	assert.Equal(t, true, resp["mandatory"])
	_, hasURL := resp["download_url"] // 빈 값 → omitempty 로 생략
	assert.False(t, hasURL)
}
