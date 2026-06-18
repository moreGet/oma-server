// Package authout 는 auth 도메인의 out 포트 구현(비밀번호 해시 등)을 담는다.
// 도메인 패키지 auth 와 구분하기 위해 패키지명을 authout 으로 둔다.
package authout

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainauth.PasswordHasher = (*BcryptHasher)(nil)

// BcryptHasher 는 bcrypt 기반 domainauth.PasswordHasher 구현이다.
type BcryptHasher struct {
	cost int
}

// NewBcryptHasher 는 BcryptHasher 를 생성한다.
// cost <= 0 이면 bcrypt.DefaultCost 를 사용한다.
func NewBcryptHasher(cost int) *BcryptHasher {
	if cost <= 0 {
		cost = bcrypt.DefaultCost
	}
	return &BcryptHasher{cost: cost}
}

// Hash 는 평문 비밀번호를 bcrypt 해시로 변환한다.
func (h *BcryptHasher) Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return string(b), nil
}

// Compare 는 해시와 평문을 비교한다. 불일치 시 domainauth.ErrInvalidCredentials.
func (h *BcryptHasher) Compare(hash, plain string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	if err == nil {
		return nil
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return domainauth.ErrInvalidCredentials
	}
	return fmt.Errorf("bcrypt compare: %w", err)
}
