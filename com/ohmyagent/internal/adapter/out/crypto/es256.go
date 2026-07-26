package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainagentregistry.TokenSigner = (*ES256Signer)(nil)

// ES256Signer 는 A2A 브로커용 ECDSA P-256 키 생성·compact JWT 서명 어댑터다.
// §공유 계약: ES256 은 Go stdlib(crypto/ecdsa)와 .NET BCL(ECDsa) 양쪽에서
// 외부 의존성 없이 검증 가능하다(서명 자체는 기존 golang-jwt/v5 재사용).
type ES256Signer struct{}

// NewES256Signer 는 ES256Signer 를 생성한다.
func NewES256Signer() *ES256Signer { return &ES256Signer{} }

// GenerateKey 는 P-256 키쌍과 새 kid 를 생성한다.
// 개인키는 PKCS#8 PEM, 공개키는 SPKI PEM(.NET `ECDsa.ImportSubjectPublicKeyInfo` 호환).
func (s *ES256Signer) GenerateKey() (domainagentregistry.A2AKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return domainagentregistry.A2AKey{}, fmt.Errorf("generate p-256 key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return domainagentregistry.A2AKey{}, fmt.Errorf("marshal private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return domainagentregistry.A2AKey{}, fmt.Errorf("marshal public key: %w", err)
	}
	return domainagentregistry.A2AKey{
		KID:           uuid.NewString(),
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})),
		PublicKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
	}, nil
}

// Mint 는 §공유 계약 클레임(iss/sub/cid/aud/iat/exp/jti, 헤더 kid)으로 ES256 compact JWT 를 서명한다.
func (s *ES256Signer) Mint(key domainagentregistry.A2AKey, c domainagentregistry.A2AClaims) (string, error) {
	priv, err := parseECPrivateKeyPEM(key.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{
		"iss": c.Issuer,
		"sub": c.Subject,
		"aud": c.Audience,
		"iat": c.IssuedAt.Unix(),
		"exp": c.ExpiresAt.Unix(),
		"jti": c.JTI,
	}
	if c.CallerAgentID != "" {
		claims["cid"] = c.CallerAgentID
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = key.KID
	signed, err := tok.SignedString(priv)
	if err != nil {
		return "", fmt.Errorf("sign a2a jwt: %w", err)
	}
	return signed, nil
}

// parseECPrivateKeyPEM 은 PKCS#8 PEM 을 *ecdsa.PrivateKey 로 복원한다.
func parseECPrivateKeyPEM(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("a2a key: invalid private key pem")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("a2a key: parse private key: %w", err)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("a2a key: not an ecdsa private key")
	}
	return priv, nil
}
