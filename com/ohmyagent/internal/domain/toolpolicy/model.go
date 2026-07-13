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

// --- 멤버별 도구 정책(전역에 계층 병합되는 멤버 오버라이드) ---

// MemberPolicy 는 한 멤버의 도구 노출/실행 오버라이드다(모드는 전역 전용이라 보관하지 않음).
// 빈 Enabled/Disabled = 오버라이드 없음(전역만 적용).
type MemberPolicy struct {
	MemberID  string
	Enabled   []string
	Disabled  []string
	UpdatedAt int64
	UpdatedBy string
}

// MemberUpdateCommand 는 어드민의 멤버 도구 정책 갱신 명령이다.
type MemberUpdateCommand struct {
	MemberID string
	Enabled  []string
	Disabled []string
	ActorID  string
}

// Normalize 는 공백 정리 + 빈 항목 제거를 한다(저장 전 호출).
func (p *MemberPolicy) Normalize() {
	p.Enabled = cleanList(p.Enabled)
	p.Disabled = cleanList(p.Disabled)
}

// IsEmpty 는 멤버 오버라이드가 비어 있는지(=전역만 적용) 반환한다.
func (p *MemberPolicy) IsEmpty() bool { return len(p.Enabled) == 0 && len(p.Disabled) == 0 }

// MemberPolicyRepository — out 포트(멤버별 도구 정책 영속화).
type MemberPolicyRepository interface {
	// Get 은 멤버 정책을 반환한다(없으면 빈 MemberPolicy{MemberID}).
	Get(ctx context.Context, memberID string) (MemberPolicy, error)
	// All 은 모든 멤버 정책을 반환한다(매니저 캐시 적재용).
	All(ctx context.Context) ([]MemberPolicy, error)
	// Save 는 멤버 정책을 upsert 한다(빈 오버라이드면 Delete 호출이 권장).
	Save(ctx context.Context, p MemberPolicy) error
	// Delete 는 멤버 정책 행을 제거한다(오버라이드 해제).
	Delete(ctx context.Context, memberID string) error
}

// ResolveEffective 는 전역 정책과 멤버 오버라이드를 계층 병합한다(전역=보안 하한).
// 모드는 전역 전용, disabled=전역∪멤버(추가 차단만), enabled=둘 다 비면 nil / 한쪽만 그쪽 / 둘 다면 교집합.
func ResolveEffective(global Settings, member *MemberPolicy) (mode string, enabled, disabled []string) {
	mode = NormMode(global.Mode)
	if member == nil || member.IsEmpty() {
		return mode, global.Enabled, global.Disabled
	}
	disabled = unionStrings(global.Disabled, member.Disabled)
	switch {
	case len(global.Enabled) == 0 && len(member.Enabled) == 0:
		enabled = nil
	case len(global.Enabled) == 0:
		enabled = member.Enabled
	case len(member.Enabled) == 0:
		enabled = global.Enabled
	default:
		enabled = intersectStrings(global.Enabled, member.Enabled)
	}
	return mode, enabled, disabled
}

// unionStrings 는 a 다음에 b 의 신규 항목을 이어붙인 합집합(순서 보존, 중복 제거)이다.
func unionStrings(a, b []string) []string {
	if len(a) == 0 {
		return cloneNonEmpty(b)
	}
	if len(b) == 0 {
		return cloneNonEmpty(a)
	}
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, list := range [2][]string{a, b} {
		for _, v := range list {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// intersectStrings 는 a 와 b 에 모두 있는 항목을 a 의 순서로 반환한다(교집합).
func intersectStrings(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	inB := make(map[string]struct{}, len(b))
	for _, v := range b {
		inB[v] = struct{}{}
	}
	out := make([]string, 0, len(a))
	seen := make(map[string]struct{}, len(a))
	for _, v := range a {
		if _, ok := inB[v]; !ok {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// cloneNonEmpty 는 슬라이스를 복사해 반환한다(nil 은 nil 로).
func cloneNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
