// Package toolpolicy 는 서버 제어형 도구 정책(노출/실행 허용·차단) + 위험명령/경로 차단 패턴의
// 도메인 모델·포트를 담는다. 값은 DB(단일 설정 행)에 보관하고 어드민이 편집한다.
package toolpolicy

import (
	"context"
	"strings"
)

// BlockedPattern 은 차단할 명령 패턴 1건이다(JSON 저장/어드민 편집/응답 공용 태그).
type BlockedPattern struct {
	Type       string `json:"type"`             // regex | substring
	Pattern    string `json:"pattern"`          //
	Reason     string `json:"reason,omitempty"` //
	ScriptType string `json:"script_type"`      // any | powershell | cmd
}

// BlockedPath 은 차단할 경로 패턴 1건이다.
type BlockedPath struct {
	Type    string `json:"type"`             // regex | substring
	Pattern string `json:"pattern"`          //
	Reason  string `json:"reason,omitempty"` //
}

// Settings 는 도구 정책 전체 스냅샷(단일 설정 행)이다.
type Settings struct {
	Mode            string // cached | realtime
	Enabled         []string
	Disabled        []string
	BlockedPatterns []BlockedPattern
	BlockedPaths    []BlockedPath
	UpdatedAt       int64
	UpdatedBy       string
}

// DefaultSettings 는 빈 정책(전체 허용 + 차단 패턴 없음)을 반환한다.
func DefaultSettings() Settings { return Settings{Mode: "cached"} }

// UpdateCommand 는 어드민의 도구 정책 갱신 명령이다.
type UpdateCommand struct {
	Settings Settings
	ActorID  string
}

// SettingsRepository — out 포트(단일 행 도구 정책 영속화).
type SettingsRepository interface {
	// Get 은 현재 정책을 반환한다(없으면 DefaultSettings).
	Get(ctx context.Context) (Settings, error)
	// Save 는 정책을 upsert(id=1) 한다.
	Save(ctx context.Context, s Settings) error
}

// Normalize 는 공백 정리 + 안전 기본값 적용 + 빈 항목 제거를 한다(저장 전 호출).
func (s *Settings) Normalize() {
	s.Mode = NormMode(s.Mode)
	s.Enabled = cleanList(s.Enabled)
	s.Disabled = cleanList(s.Disabled)

	patterns := make([]BlockedPattern, 0, len(s.BlockedPatterns))
	for _, p := range s.BlockedPatterns {
		p.Pattern = strings.TrimSpace(p.Pattern)
		if p.Pattern == "" {
			continue
		}
		p.Type = NormMatchType(p.Type)
		p.ScriptType = NormScriptType(p.ScriptType)
		p.Reason = strings.TrimSpace(p.Reason)
		patterns = append(patterns, p)
	}
	s.BlockedPatterns = patterns

	paths := make([]BlockedPath, 0, len(s.BlockedPaths))
	for _, p := range s.BlockedPaths {
		p.Pattern = strings.TrimSpace(p.Pattern)
		if p.Pattern == "" {
			continue
		}
		p.Type = NormMatchType(p.Type)
		p.Reason = strings.TrimSpace(p.Reason)
		paths = append(paths, p)
	}
	s.BlockedPaths = paths
}

// NormMode 는 정책 모드를 정규화한다(realtime 만 그대로, 그 외=cached).
func NormMode(m string) string {
	if strings.TrimSpace(m) == "realtime" {
		return "realtime"
	}
	return "cached"
}

// NormMatchType 은 매칭 방식을 정규화한다(regex 만 그대로, 그 외=substring 안전 기본).
func NormMatchType(t string) string {
	if strings.TrimSpace(t) == "regex" {
		return "regex"
	}
	return "substring"
}

// NormScriptType 은 적용 셸을 정규화한다(powershell|cmd 만 그대로, 그 외=any).
func NormScriptType(s string) string {
	switch strings.TrimSpace(s) {
	case "powershell", "cmd":
		return strings.TrimSpace(s)
	default:
		return "any"
	}
}

// cleanList 는 각 항목을 trim 하고 빈 항목을 제거한다(중복은 유지).
func cleanList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
