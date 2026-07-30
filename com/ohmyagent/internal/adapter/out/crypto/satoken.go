package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	domainserviceaccount "aiagent/com/ohmyagent/internal/domain/serviceaccount"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainserviceaccount.TokenHasher = (*SATokenHasher)(nil)

// saTokenRandomBytes 는 발급 토큰의 랜덤 엔트로피 바이트 수다(256비트).
const saTokenRandomBytes = 32

// SATokenHasher 는 불투명 서비스 계정 토큰을 생성하고 SHA-256 hex 로 해시한다.
// 서버는 평문 토큰을 저장하지 않으며, 해시만 보관해 인증 시 대조한다.
type SATokenHasher struct{}

// NewSATokenHasher 는 SATokenHasher 를 생성한다(상태 없음).
func NewSATokenHasher() *SATokenHasher { return &SATokenHasher{} }

// NewToken 은 "oma_sa_<random>" 평문과 그 SHA-256 hex 해시를 함께 반환한다.
// 랜덤은 crypto/rand 32바이트 → base64url(no padding) 로 인코딩한다.
func (h *SATokenHasher) NewToken() (plain, hash string, err error) {
	buf := make([]byte, saTokenRandomBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", "", fmt.Errorf("crypto: sa token random: %w", err)
	}
	plain = domainserviceaccount.TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return plain, h.Hash(plain), nil
}

// Hash 는 raw 토큰(접두사 포함)을 SHA-256 hex(64자) 로 해시한다.
func (h *SATokenHasher) Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
