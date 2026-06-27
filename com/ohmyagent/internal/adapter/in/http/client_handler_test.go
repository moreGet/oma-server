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
	h := NewClientHandler(ToolPolicyConfig{Mode: "", Disabled: []string{"kill_process"}}, ClientVersionConfig{})
	rec := httptest.NewRecorder()
	require.NoError(t, h.ToolsPolicy(rec, httptest.NewRequest("GET", "/api/v1/tools/policy", nil)))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "cached", resp["mode"]) // 빈/미지정 → cached
	assert.Nil(t, resp["enabled"])          // nil → null = 전체 허용
}

func TestClientHandler_Authorize(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{Enabled: []string{"read_file"}, Disabled: []string{"kill_process"}}, ClientVersionConfig{})

	a, reason := h.authorize("kill_process") // disabled 우선
	assert.False(t, a)
	assert.NotNil(t, reason)

	a, _ = h.authorize("read_file") // enabled 화이트리스트 포함
	assert.True(t, a)

	a, _ = h.authorize("write_file") // enabled 지정인데 미포함 → 차단
	assert.False(t, a)

	open := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{}) // 목록 없음 → 전체 허용
	a, _ = open.authorize("anything")
	assert.True(t, a)
}

func TestClientHandler_AuthorizeEndpoint(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{Disabled: []string{"kill_process"}}, ClientVersionConfig{})
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

func TestClientHandler_Version(t *testing.T) {
	h := NewClientHandler(ToolPolicyConfig{}, ClientVersionConfig{Latest: "1.4.0", MinimumSupported: "1.2.0", Mandatory: true})
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
