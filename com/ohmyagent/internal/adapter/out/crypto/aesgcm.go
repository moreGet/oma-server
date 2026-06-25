// Package crypto 는 시크릿(예: Provider API 키)의 대칭키 암복호화 out 어댑터를 제공한다.
// AES-256-GCM 을 사용하며, 키는 설정 시크릿의 SHA-256 해시(32바이트)로 파생한다.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	domainllmprovider "aiagent/com/ohmyagent/internal/domain/llmprovider"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainllmprovider.Cipher = (*AESGCMCipher)(nil)

// ErrDisabled 는 암호화 시크릿이 설정되지 않아 암복호화를 사용할 수 없을 때 반환된다.
var ErrDisabled = errors.New("encryption is not configured (set APP_ENCRYPTION_SECRET)")

// AESGCMCipher 는 AES-256-GCM 기반 domainllmprovider.Cipher 구현이다.
// secret 이 비어 있으면 비활성 상태가 되어 Encrypt/Decrypt 가 ErrDisabled 를 반환한다.
type AESGCMCipher struct {
	key     []byte // 32바이트(AES-256)
	enabled bool
}

// NewAESGCMCipher 는 시크릿으로부터 cipher 를 생성한다. secret 이 비면 비활성.
func NewAESGCMCipher(secret string) *AESGCMCipher {
	if secret == "" {
		return &AESGCMCipher{enabled: false}
	}
	sum := sha256.Sum256([]byte(secret))
	return &AESGCMCipher{key: sum[:], enabled: true}
}

// Enabled 는 암호화 사용 가능 여부를 반환한다.
func (c *AESGCMCipher) Enabled() bool { return c.enabled }

// Encrypt 는 평문을 AES-GCM 으로 암호화하고 base64(nonce||ciphertext) 를 반환한다.
func (c *AESGCMCipher) Encrypt(plaintext string) (string, error) {
	if !c.enabled {
		return "", ErrDisabled
	}
	gcm, err := c.newGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 는 base64(nonce||ciphertext) 를 복호화해 평문을 반환한다.
func (c *AESGCMCipher) Decrypt(ciphertext string) (string, error) {
	if !c.enabled {
		return "", ErrDisabled
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("crypto: base64 decode: %w", err)
	}
	gcm, err := c.newGCM()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("crypto: ciphertext too short")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w", err)
	}
	return string(plain), nil
}

func (c *AESGCMCipher) newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	return gcm, nil
}
