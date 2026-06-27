package authout

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

func TestBcryptHasher_HashCompare(t *testing.T) {
	h := NewBcryptHasher(4) // 테스트는 낮은 cost 로 빠르게

	hash, err := h.Hash("p@ssw0rd")
	require.NoError(t, err)
	assert.NotEqual(t, "p@ssw0rd", hash) // 평문이 그대로 저장되지 않는다

	require.NoError(t, h.Compare(hash, "p@ssw0rd")) // 일치
}

func TestBcryptHasher_CompareMismatch(t *testing.T) {
	h := NewBcryptHasher(4)
	hash, err := h.Hash("correct")
	require.NoError(t, err)

	err = h.Compare(hash, "wrong")
	assert.ErrorIs(t, err, domainauth.ErrInvalidCredentials) // 불일치 → 도메인 에러로 정규화
}

func TestBcryptHasher_DefaultCost(t *testing.T) {
	// cost <= 0 이면 기본 cost 사용(패닉/에러 없이 정상 동작).
	h := NewBcryptHasher(0)
	hash, err := h.Hash("x")
	require.NoError(t, err)
	require.NoError(t, h.Compare(hash, "x"))
}
