// Package serviceaccount 은 서비스 계정(비대화형 계정)과 장수 API 키 어그리거트의
// 도메인 모델을 정의한다. 외부 의존 없이 stdlib 만 사용한다(순수 도메인).
package serviceaccount

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러(핸들러가 HTTP 로 매핑) ---
var (
	// ErrNotFound 는 서비스 계정을 찾지 못했을 때 반환한다(→ 404).
	ErrNotFound = errors.New("service account not found")
	// ErrKeyNotFound 는 서비스 계정 키를 찾지 못했을 때 반환한다(→ 404).
	ErrKeyNotFound = errors.New("service account key not found")
	// ErrInvalidKey 는 인증 경로 전용 에러다(→ 401).
	// 폐기·만료·계정폐기·미존재를 모두 뭉개 존재를 은닉하고 항상 401 로 수렴시킨다.
	ErrInvalidKey = errors.New("invalid service account key")
)

// ErrValidation 은 입력 검증 실패다(→ 400).
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// TokenPrefix 는 불투명 API 키 접두사다(확정 결정 3).
// 라우터가 이 접두사로 API키/JWT 경로를 분기하고, 발급 시 평문 토큰 앞에 붙는다.
const TokenPrefix = "oma_sa_"

// --- 엔티티 ---

// ServiceAccount 는 사람과 구분되는 비대화형 계정이다(별도 id 공간, member 로 흡수 안 함).
type ServiceAccount struct {
	ID            string    // UUID v4 (별도 id 공간)
	Name          string    // 표시 이름
	Description   string    // 설명(선택)
	OwnerMemberID string    // 폐기 책임자(실재 member 검증)
	CreatedAt     time.Time // 생성 시각
	UpdatedAt     time.Time // 갱신 시각
	CreatedBy     string    // 생성 admin actor id ("" = 시스템)
	RevokedAt     time.Time // zero = 활성 (0 sentinel)
}

// Revoked 는 계정 폐기 여부를 반환한다.
func (a ServiceAccount) Revoked() bool { return !a.RevokedAt.IsZero() }

// ServiceAccountKey 는 계정에 딸린 장수 API 키다(평문 미보관, SHA-256 해시만 저장).
type ServiceAccountKey struct {
	ID         string    // UUID v4 (= key_id)
	AccountID  string    // sa_id
	TokenHash  string    // SHA-256 hex(64). 평문 미보관
	CreatedAt  time.Time // 발급 시각
	ExpiresAt  time.Time // zero = 무기한 (0 sentinel, 확정 결정/스펙 §2B)
	LastUsedAt time.Time // zero = 미사용
	RevokedAt  time.Time // zero = 활성
	CreatedBy  string    // 발급 admin actor id
}

// IsUsable 은 무효화(폐기/만료) 판정 순수 함수다.
func (k ServiceAccountKey) IsUsable(now time.Time) bool {
	if !k.RevokedAt.IsZero() {
		return false
	}
	if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
		return false
	}
	return true
}

// Revoked 는 키 폐기 여부를 반환한다.
func (k ServiceAccountKey) Revoked() bool { return !k.RevokedAt.IsZero() }

// --- 커맨드 ---

// CreateAccountCommand 는 POST /service-accounts 입력이다.
type CreateAccountCommand struct {
	ActorID       string // admin
	Name          string
	OwnerMemberID string
	Description   string
}

// Normalize 는 입력 문자열의 공백을 정리한다.
func (c *CreateAccountCommand) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.OwnerMemberID = strings.TrimSpace(c.OwnerMemberID)
	c.Description = strings.TrimSpace(c.Description)
}

// Validate 는 정규화 후 필수값을 검증한다.
func (c *CreateAccountCommand) Validate() error {
	c.Normalize()
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	if c.OwnerMemberID == "" {
		return &ErrValidation{Msg: "owner_member_id is required"}
	}
	return nil
}

// IssueKeyCommand 는 POST /service-accounts/{id}/keys 입력이다.
type IssueKeyCommand struct {
	ActorID   string    // admin
	AccountID string    // 대상 계정
	ExpiresAt time.Time // zero = 무기한
}

// Validate 는 ExpiresAt 이 지정되면 미래여야 함을 검증한다(과거·현재 → 400).
// 90일 하한은 클라이언트 운영 가이드이며 서버는 강제하지 않는다.
func (c *IssueKeyCommand) Validate(now time.Time) error {
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now) {
		return &ErrValidation{Msg: "expires_at must be in the future"}
	}
	return nil
}
