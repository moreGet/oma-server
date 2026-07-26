package agentregistry

import (
	"context"
	"time"
)

// Service 는 레지스트리 유스케이스 포트(in)다. HTTP 핸들러가 소비한다.
type Service interface {
	// Register 는 (owner, name) 업서트로 에이전트를 등록한다(재등록 시 기존 agent_id 유지).
	Register(ctx context.Context, cmd RegisterCommand) (Agent, error)
	// Heartbeat 은 생존 신호를 기록한다. 없는 id 는 ErrNotFound, 소유자 불일치는 ErrForbidden.
	Heartbeat(ctx context.Context, id, ownerID string) error
	// Deregister 는 우아한 해제다(소유자만).
	Deregister(ctx context.Context, id, ownerID string) error
	// Discover 는 필터 + 생존성 계산으로 에이전트를 발견한다(기본 online+stale).
	Discover(ctx context.Context, f Filter) ([]Agent, error)
	// Get 은 단건 조회다(status 계산 포함).
	Get(ctx context.Context, id string) (Agent, error)
	// MintToken 은 대상 에이전트 호출용 단명 ES256 토큰을 발급한다(A2A 브로커).
	// 대상 미존재 시 ErrNotFound.
	MintToken(ctx context.Context, callerMemberID, targetAgentID string) (A2AToken, error)
	// PublicKey 는 활성 서명 키의 공개 정보를 반환한다(수신측 검증용).
	PublicKey() (kid, alg, publicKeyPEM string)
	// LeaseTTL/HeartbeatInterval 은 register/heartbeat 응답으로 클라이언트 루프 주기를 결정한다.
	LeaseTTL() time.Duration
	HeartbeatInterval() time.Duration
}

// Repository 는 에이전트 레코드 영속화 포트(out)다.
type Repository interface {
	// Upsert 는 (owner_member_id, name) 유니크 기준 업서트다.
	// 기존 행이 있으면 id/created_at 을 유지한 채 endpoint/capabilities 등을 갱신하고,
	// 항상 확정된(기존 또는 신규) 레코드를 반환한다.
	Upsert(ctx context.Context, a Agent) (Agent, error)
	// Get 은 id 단건 조회다. 없으면 ErrNotFound.
	Get(ctx context.Context, id string) (Agent, error)
	// Touch 는 heartbeat 시각을 갱신한다. (id, ownerID) 불일치 행이 없으면 ErrNotFound.
	Touch(ctx context.Context, id, ownerID string, now time.Time) error
	// Delete 는 소유자 스코프 삭제다. 대상 없으면 ErrNotFound.
	Delete(ctx context.Context, id, ownerID string) error
	// DeleteByID 는 소유자 무관 삭제다(어드민 강제 해제 전용).
	DeleteByID(ctx context.Context, id string) error
	// DeleteHeartbeatBefore 는 cutoff 이전 heartbeat 레코드를 일괄 삭제한다(sweeper). 삭제 수 반환.
	DeleteHeartbeatBefore(ctx context.Context, cutoff time.Time) (int, error)
	// List 는 코스 필터(SQL where — owner/capability/tag/query/exclude)를 적용해 조회한다.
	// 정밀 일치·status 판정은 호출자(유스케이스)가 Matches/ComputeStatus 로 재검증한다.
	List(ctx context.Context, f Filter) ([]Agent, error)
}

// KeyRepository 는 A2A 브로커 서명 키 영속화 포트(out)다.
// PrivateKeyPEM 은 유스케이스가 Cipher 로 암호화한 값을 그대로 저장/반환한다(레포는 암복호화하지 않음).
type KeyRepository interface {
	// GetActive 는 활성 키를 반환한다. 없으면 ErrNotFound(→ 기동 bootstrap 이 생성).
	GetActive(ctx context.Context) (A2AKey, error)
	Save(ctx context.Context, k A2AKey) error
}

// TokenSigner 는 ES256 키 생성·서명 포트(out)다(crypto 어댑터가 구현).
type TokenSigner interface {
	// GenerateKey 는 P-256 키쌍과 새 kid 를 생성한다(PEM 평문).
	GenerateKey() (A2AKey, error)
	// Mint 는 클레임을 ES256 compact JWT 로 서명한다(헤더에 kid).
	Mint(key A2AKey, c A2AClaims) (string, error)
}

// Cipher 는 개인키 PEM 암복호화 포트다(Provider api_key 와 동일한 AES-GCM 어댑터 재사용).
type Cipher interface {
	Encrypt(plain string) (string, error)
	Decrypt(enc string) (string, error)
}

// MemberDirectory 는 owner id → username 해석 포트다(어드민 표시 전용).
type MemberDirectory interface {
	UsernamesByIDs(ctx context.Context, ids []string) (map[string]string, error)
}
