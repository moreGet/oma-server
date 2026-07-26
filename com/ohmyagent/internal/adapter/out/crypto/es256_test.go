package crypto

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// TestES256SignerRoundTrip 은 브로커 왕복을 검증한다:
// GenerateKey 로 만든 키로 Mint 한 compact JWT 가 public_key_pem 만으로
// 서명·클레임(iss/sub/cid/aud/exp/jti)·헤더 kid 까지 검증되는지(수신 에이전트 관점).
func TestES256SignerRoundTrip(t *testing.T) {
	s := NewES256Signer()

	key, err := s.GenerateKey()
	require.NoError(t, err)
	assert.NotEmpty(t, key.KID)
	assert.Contains(t, key.PrivateKeyPEM, "PRIVATE KEY")
	assert.Contains(t, key.PublicKeyPEM, "PUBLIC KEY")

	now := time.Now().UTC().Truncate(time.Second)
	claims := domainagentregistry.A2AClaims{
		Issuer:        domainagentregistry.A2AIssuer,
		Subject:       "member-1",
		CallerAgentID: "agent-caller",
		Audience:      "agent-target",
		IssuedAt:      now,
		ExpiresAt:     now.Add(120 * time.Second),
		JTI:           "jti-1",
	}
	signed, err := s.Mint(key, claims)
	require.NoError(t, err)

	// 수신측 검증 시뮬레이션: 공개키 PEM(SPKI)만으로 파싱·검증(alg 는 ES256 으로 고정).
	pub := parsePublicKeyPEM(t, key.PublicKeyPEM)
	parsed, err := jwt.Parse(signed, func(tok *jwt.Token) (any, error) {
		return pub, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("agent-target"), jwt.WithIssuer(domainagentregistry.A2AIssuer))
	require.NoError(t, err)
	require.True(t, parsed.Valid)

	assert.Equal(t, key.KID, parsed.Header["kid"], "헤더 kid 로 키 회전 추종")
	mc := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, "member-1", mc["sub"])
	assert.Equal(t, "agent-caller", mc["cid"])
	assert.Equal(t, "jti-1", mc["jti"])
	exp, err := mc.GetExpirationTime()
	require.NoError(t, err)
	assert.Equal(t, now.Add(120*time.Second), exp.Time.UTC())

	t.Run("aud 불일치 거부(대상 아닌 에이전트가 수신)", func(t *testing.T) {
		_, err := jwt.Parse(signed, func(tok *jwt.Token) (any, error) { return pub, nil },
			jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("agent-other"))
		assert.Error(t, err)
	})

	t.Run("다른 키의 서명 거부", func(t *testing.T) {
		other, err := s.GenerateKey()
		require.NoError(t, err)
		otherPub := parsePublicKeyPEM(t, other.PublicKeyPEM)
		_, err = jwt.Parse(signed, func(tok *jwt.Token) (any, error) { return otherPub, nil },
			jwt.WithValidMethods([]string{"ES256"}))
		assert.Error(t, err)
	})

	t.Run("만료 토큰 거부", func(t *testing.T) {
		expired := claims
		expired.IssuedAt = now.Add(-10 * time.Minute)
		expired.ExpiresAt = now.Add(-8 * time.Minute)
		signedExpired, err := s.Mint(key, expired)
		require.NoError(t, err)
		_, err = jwt.Parse(signedExpired, func(tok *jwt.Token) (any, error) { return pub, nil },
			jwt.WithValidMethods([]string{"ES256"}))
		assert.Error(t, err)
	})

	t.Run("cid 는 미등록 호출자면 생략", func(t *testing.T) {
		anon := claims
		anon.CallerAgentID = ""
		signedAnon, err := s.Mint(key, anon)
		require.NoError(t, err)
		parsed, err := jwt.Parse(signedAnon, func(tok *jwt.Token) (any, error) { return pub, nil },
			jwt.WithValidMethods([]string{"ES256"}))
		require.NoError(t, err)
		_, has := parsed.Claims.(jwt.MapClaims)["cid"]
		assert.False(t, has)
	})
}

// TestES256GenerateKeyUnique 는 키/kid 가 호출마다 새로 발급되는지 확인한다(회전 대비).
func TestES256GenerateKeyUnique(t *testing.T) {
	s := NewES256Signer()
	k1, err := s.GenerateKey()
	require.NoError(t, err)
	k2, err := s.GenerateKey()
	require.NoError(t, err)
	assert.NotEqual(t, k1.KID, k2.KID)
	assert.NotEqual(t, k1.PrivateKeyPEM, k2.PrivateKeyPEM)
}

func parsePublicKeyPEM(t *testing.T, pemStr string) *ecdsa.PublicKey {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	require.NotNil(t, block)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	ec, ok := pub.(*ecdsa.PublicKey)
	require.True(t, ok)
	return ec
}
