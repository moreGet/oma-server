package crypto

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESGCM_RoundTrip(t *testing.T) {
	c := NewAESGCMCipher("super-secret-key")
	require.True(t, c.Enabled())

	for _, plain := range []string{"", "sk-12345", "한글 비밀 키 🔐", "a longer secret with spaces"} {
		enc, err := c.Encrypt(plain)
		require.NoError(t, err)
		assert.NotEqual(t, plain, enc) // 암호문은 평문과 다르다

		dec, err := c.Decrypt(enc)
		require.NoError(t, err)
		assert.Equal(t, plain, dec)
	}
}

func TestAESGCM_NonceRandomized(t *testing.T) {
	c := NewAESGCMCipher("k")
	a, err := c.Encrypt("same")
	require.NoError(t, err)
	b, err := c.Encrypt("same")
	require.NoError(t, err)
	assert.NotEqual(t, a, b) // 같은 평문도 nonce 가 달라 암호문이 다르다
}

func TestAESGCM_Disabled(t *testing.T) {
	c := NewAESGCMCipher("")
	assert.False(t, c.Enabled())

	_, err := c.Encrypt("x")
	assert.ErrorIs(t, err, ErrDisabled)
	_, err = c.Decrypt("x")
	assert.ErrorIs(t, err, ErrDisabled)
}

func TestAESGCM_DecryptErrors(t *testing.T) {
	c := NewAESGCMCipher("k")

	_, err := c.Decrypt("!!!not-base64!!!")
	require.Error(t, err)

	_, err = c.Decrypt("YWJj") // base64 "abc" → nonce 보다 짧음
	require.Error(t, err)

	// 다른 키로 만든 암호문은 복호화 실패(인증 태그 불일치).
	enc, err := NewAESGCMCipher("key-A").Encrypt("secret")
	require.NoError(t, err)
	_, err = NewAESGCMCipher("key-B").Decrypt(enc)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrDisabled))
}
