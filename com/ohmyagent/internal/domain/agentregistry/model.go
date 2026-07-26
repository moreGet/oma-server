// Package agentregistry 는 헤드리스 에이전트 레지스트리(등록·발견·생존성·A2A 토큰 브로커) 도메인이다.
//
// 주의: 기존 domain/agent 는 LLM 중계 루프 도메인이고, 이 패키지는 별개다 —
// 에이전트 간 실제 대화(A2A)는 에이전트끼리 직접 SSE 로 하고,
// 이 서버는 전화번호부(등록·발견·생존성)와 호출 토큰 발급(브로커)만 담당한다(v1, 중계 relay 아님).
package agentregistry

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// --- 도메인 에러: 핸들러가 HTTP 코드로 매핑하는 계약 ---

// ErrNotFound 는 에이전트/활성 키 없음이다(→404).
var ErrNotFound = errors.New("agent not found")

// ErrForbidden 은 소유자 불일치다(→403. 단, §공유 계약상 heartbeat/delete 응답은
// 404 로 매핑해 존재 여부를 은닉한다 — 클라이언트는 404 를 재-register 신호로 삼는다).
var ErrForbidden = errors.New("not the agent owner")

// ErrValidation 은 입력 검증 실패다(→400).
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// --- 생존성(liveness) ---

// Status 는 서버가 read 시 heartbeat 경과로 계산하는 생존성이다(§공유 계약 enum 문자열).
type Status string

const (
	StatusOnline  Status = "online"
	StatusStale   Status = "stale"
	StatusOffline Status = "offline"
)

// staleMultiplier: lease TTL 의 몇 배까지를 stale 로 볼지(그 이상은 offline). §공유 계약: 3×lease_ttl.
const staleMultiplier = 3

// ComputeStatus 는 마지막 heartbeat 경과 시간으로 생존성을 판정한다(read 시 계산 — 별도 스케줄러 불필요).
//   - online:  now - lastHeartbeat < leaseTTL
//   - stale:   < 3×leaseTTL
//   - offline: 그 이상(또는 heartbeat 기록 없음 / TTL 미설정)
func ComputeStatus(lastHeartbeat, now time.Time, leaseTTL time.Duration) Status {
	if leaseTTL <= 0 || lastHeartbeat.IsZero() {
		return StatusOffline
	}
	elapsed := now.Sub(lastHeartbeat)
	switch {
	case elapsed < leaseTTL:
		return StatusOnline
	case elapsed < staleMultiplier*leaseTTL:
		return StatusStale
	default:
		return StatusOffline
	}
}

// --- 엔티티 ---

// 필드 길이 상한(스키마 VARCHAR 와 일치).
const (
	maxNameLen     = 255
	maxEndpointLen = 2048
	maxModelLen    = 255
	maxVersionLen  = 64
	maxTagLen      = 128
	maxTagCount    = 32
)

// Agent 는 등록된 헤드리스 에이전트 레코드다.
type Agent struct {
	ID              string // 서버 발급 UUID v4 (§계약 agent_id)
	OwnerID         string // 등록한 멤버 id (JWT claims). (OwnerID, Name) 이 업서트 키
	Name            string
	EndpointURL     string   // A2A 리스너 base URL (http/https 절대 URL)
	Capabilities    []string // 발견 필터 키(자유 태그)
	Tags            []string // 환경/그룹 태그(선택)
	Model           string   // 주 모델 id(선택)
	Version         string   // Host 버전(선택)
	LastHeartbeatAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Status 는 저장하지 않고 read 시 ComputeStatus 로 채우는 파생 값이다.
	Status Status
	// OwnerName 은 어드민 표시용 파생 값이다(MemberDirectory 해석 시 채움. 저장·API 미노출).
	OwnerName string
}

// --- 커맨드 ---

// RegisterCommand 는 등록(업서트) 입력이다(§계약 POST /agents/register).
type RegisterCommand struct {
	OwnerID      string // JWT claims 의 member id
	Name         string
	EndpointURL  string
	Capabilities []string
	Tags         []string
	Model        string
	Version      string
}

// Normalize 는 공백을 정리하고 빈 태그를 제거한다.
func (c *RegisterCommand) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.EndpointURL = strings.TrimRight(strings.TrimSpace(c.EndpointURL), "/")
	c.Model = strings.TrimSpace(c.Model)
	c.Version = strings.TrimSpace(c.Version)
	c.Capabilities = normalizeTags(c.Capabilities)
	c.Tags = normalizeTags(c.Tags)
}

// Validate 는 필수 필드·형식·길이를 검증한다.
func (c *RegisterCommand) Validate() error {
	c.Normalize()
	if c.OwnerID == "" {
		return &ErrValidation{Msg: "owner is required"}
	}
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	if len(c.Name) > maxNameLen {
		return &ErrValidation{Msg: "name is too long"}
	}
	if err := validateEndpointURL(c.EndpointURL); err != nil {
		return err
	}
	if len(c.Model) > maxModelLen {
		return &ErrValidation{Msg: "model is too long"}
	}
	if len(c.Version) > maxVersionLen {
		return &ErrValidation{Msg: "version is too long"}
	}
	if len(c.Capabilities) > maxTagCount || len(c.Tags) > maxTagCount {
		return &ErrValidation{Msg: "too many capabilities/tags"}
	}
	return nil
}

// validateEndpointURL 은 endpoint_url 이 http/https 절대 URL 인지 검증한다.
//
// SSRF 관점 정책(v1 결정): 사설/링크로컬 IP 도 허용한다 — 에이전트는 사내망 내부에 떠 있는 것이
// 정상 배치이고, 이 서버는 endpoint 를 저장·반환만 할 뿐 직접 접속하지 않는다(중계 아님).
// 서버가 endpoint 로 아웃바운드 호출을 하게 되는 시점(v2 릴레이/헬스프로브)에 차단 정책을 재검토한다.
func validateEndpointURL(raw string) error {
	if raw == "" {
		return &ErrValidation{Msg: "endpoint_url is required"}
	}
	if len(raw) > maxEndpointLen {
		return &ErrValidation{Msg: "endpoint_url is too long"}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &ErrValidation{Msg: "endpoint_url must be an absolute http/https URL"}
	}
	return nil
}

// normalizeTags 는 공백 정리·빈 값 제거·중복 제거·길이 제한을 적용한다(순서 유지).
func normalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > maxTagLen {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// --- 발견 필터 ---

// Filter 는 발견(GET /agents) 질의 필터다. Status 필터는 유스케이스가 ComputeStatus 이후 적용한다.
type Filter struct {
	OwnerID    string // 소유자 스코프(브로커 cid 해석·어드민용. 발견 API 는 미사용)
	Capability string // capabilities 정확 일치
	Tag        string // tags 정확 일치
	Status     string // "" = online+stale(기본) | online | stale | offline
	Query      string // name/model/capabilities/tags 부분 일치(대소문자 무시)
	ExcludeID  string // 자기 자신 제외(exclude_self)
}

// Normalize 는 필터 입력의 공백을 정리한다.
func (f *Filter) Normalize() {
	f.OwnerID = strings.TrimSpace(f.OwnerID)
	f.Capability = strings.TrimSpace(f.Capability)
	f.Tag = strings.TrimSpace(f.Tag)
	f.Status = strings.ToLower(strings.TrimSpace(f.Status))
	f.Query = strings.TrimSpace(f.Query)
	f.ExcludeID = strings.TrimSpace(f.ExcludeID)
}

// Validate 는 status 필터 값을 검증한다(빈 값 허용).
func (f *Filter) Validate() error {
	f.Normalize()
	switch f.Status {
	case "", string(StatusOnline), string(StatusStale), string(StatusOffline):
		return nil
	default:
		return &ErrValidation{Msg: "status must be one of {online, stale, offline}"}
	}
}

// Matches 는 생존성(Status)을 제외한 필터 일치 여부를 판정한다(순수 로직).
// 레포지토리의 SQL LIKE 프리필터는 코스 필터일 뿐, 최종 판정은 항상 이 함수가 한다.
func (a Agent) Matches(f Filter) bool {
	if f.OwnerID != "" && a.OwnerID != f.OwnerID {
		return false
	}
	if f.ExcludeID != "" && a.ID == f.ExcludeID {
		return false
	}
	if f.Capability != "" && !containsString(a.Capabilities, f.Capability) {
		return false
	}
	if f.Tag != "" && !containsString(a.Tags, f.Tag) {
		return false
	}
	if f.Query != "" && !a.matchesQuery(f.Query) {
		return false
	}
	return true
}

// matchesQuery 는 자유 텍스트를 name/model/capabilities/tags 에 대해 부분 일치(대소문자 무시)로 판정한다.
func (a Agent) matchesQuery(q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(a.Name), q) || strings.Contains(strings.ToLower(a.Model), q) {
		return true
	}
	for _, c := range a.Capabilities {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	for _, t := range a.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// --- A2A 토큰 브로커(v1) ---

// A2AIssuer 는 브로커 토큰의 iss 클레임 고정값이다(§공유 계약).
const A2AIssuer = "ohmyagent-server"

// A2AAlg 는 브로커 서명 알고리즘이다(§공유 계약: ES256 = ECDSA P-256.
// Go stdlib 과 .NET BCL 양쪽에서 외부 의존성 없이 검증 가능 — Ed25519 는 .NET BCL 미지원이라 배제).
const A2AAlg = "ES256"

// A2AKey 는 브로커 서명 키쌍이다. PrivateKeyPEM 은 메모리·어댑터 경계에서만 평문이고,
// 영속화 시에는 유스케이스가 Cipher(AES-GCM)로 암호화해 저장한다(Provider api_key 직접 저장과 동일 방식).
type A2AKey struct {
	KID           string // 키 식별자(JWT 헤더 kid). 회전 시 새 kid 발급
	PrivateKeyPEM string // P-256 개인키(PKCS#8 PEM). 로그 금지
	PublicKeyPEM  string // 공개키(SPKI PEM). 수신 에이전트가 서명 검증에 사용
	Active        bool
	CreatedAt     time.Time
}

// A2AClaims 는 브로커가 서명하는 토큰 클레임이다(§공유 계약).
type A2AClaims struct {
	Issuer        string    // iss = A2AIssuer
	Subject       string    // sub = 호출자 member id
	CallerAgentID string    // cid = 호출자 agent_id(등록된 경우. 로그 상관관계용, 빈 값 허용)
	Audience      string    // aud = 대상 agent_id
	IssuedAt      time.Time // iat
	ExpiresAt     time.Time // exp (기본 120s)
	JTI           string    // jti = uuid(재생 방지 캐시는 v1 미구현 — 로그 상관관계용)
}

// A2AToken 은 발급된 브로커 토큰이다(POST /agents/{id}/token 응답).
type A2AToken struct {
	Token           string
	ExpiresIn       time.Duration
	AudienceAgentID string
}
