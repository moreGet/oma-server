package serviceaccount

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// TestServiceAccount_Revoked 는 계정 폐기 판정(0 sentinel)을 검증한다.
func TestServiceAccount_Revoked(t *testing.T) {
	t.Run("zero RevokedAt 는 활성", func(t *testing.T) {
		a := ServiceAccount{}
		assert.False(t, a.Revoked())
	})
	t.Run("RevokedAt 지정 시 폐기", func(t *testing.T) {
		a := ServiceAccount{RevokedAt: testNow}
		assert.True(t, a.Revoked())
	})
}

// TestServiceAccountKey_IsUsable 는 폐기/만료/무기한/경계(now==ExpiresAt) 판정을 검증한다.
func TestServiceAccountKey_IsUsable(t *testing.T) {
	tests := []struct {
		name string
		key  ServiceAccountKey
		want bool
	}{
		{
			name: "무기한(ExpiresAt=0) + 미폐기 → 사용가능",
			key:  ServiceAccountKey{},
			want: true,
		},
		{
			name: "미래 만료 + 미폐기 → 사용가능",
			key:  ServiceAccountKey{ExpiresAt: testNow.Add(time.Hour)},
			want: true,
		},
		{
			name: "폐기됨(무기한이어도) → 사용불가",
			key:  ServiceAccountKey{RevokedAt: testNow.Add(-time.Hour)},
			want: false,
		},
		{
			name: "과거 만료 → 사용불가",
			key:  ServiceAccountKey{ExpiresAt: testNow.Add(-time.Second)},
			want: false,
		},
		{
			name: "경계: now == ExpiresAt → 만료(사용불가)",
			key:  ServiceAccountKey{ExpiresAt: testNow},
			want: false,
		},
		{
			name: "경계: ExpiresAt = now + 1ns → 아직 유효",
			key:  ServiceAccountKey{ExpiresAt: testNow.Add(time.Nanosecond)},
			want: true,
		},
		{
			name: "폐기 우선: 미래 만료라도 폐기면 사용불가",
			key:  ServiceAccountKey{ExpiresAt: testNow.Add(time.Hour), RevokedAt: testNow.Add(-time.Minute)},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.key.IsUsable(testNow))
		})
	}
}

// TestServiceAccountKey_Revoked 는 키 폐기 판정을 검증한다.
func TestServiceAccountKey_Revoked(t *testing.T) {
	assert.False(t, ServiceAccountKey{}.Revoked())
	assert.True(t, ServiceAccountKey{RevokedAt: testNow}.Revoked())
}

// TestCreateAccountCommand_Normalize 는 공백 정리를 검증한다.
func TestCreateAccountCommand_Normalize(t *testing.T) {
	c := CreateAccountCommand{
		Name:          "  bot  ",
		OwnerMemberID: "  m-1 ",
		Description:   "\tdesc \n",
	}
	c.Normalize()
	assert.Equal(t, "bot", c.Name)
	assert.Equal(t, "m-1", c.OwnerMemberID)
	assert.Equal(t, "desc", c.Description)
}

// TestCreateAccountCommand_Validate 는 정규화 후 필수값 검증을 확인한다.
func TestCreateAccountCommand_Validate(t *testing.T) {
	t.Run("정상", func(t *testing.T) {
		c := CreateAccountCommand{Name: "  bot ", OwnerMemberID: " m-1 "}
		require.NoError(t, c.Validate())
		assert.Equal(t, "bot", c.Name, "Validate 는 Normalize 를 내부 호출")
		assert.Equal(t, "m-1", c.OwnerMemberID)
	})
	t.Run("공백뿐인 name → ErrValidation", func(t *testing.T) {
		c := CreateAccountCommand{Name: "   ", OwnerMemberID: "m-1"}
		err := c.Validate()
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "name is required", ve.Msg)
	})
	t.Run("owner_member_id 누락 → ErrValidation", func(t *testing.T) {
		c := CreateAccountCommand{Name: "bot", OwnerMemberID: "  "}
		err := c.Validate()
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "owner_member_id is required", ve.Msg)
	})
}

// TestIssueKeyCommand_Validate 는 만료 시각 검증(과거/현재/미래/무기한)을 확인한다.
func TestIssueKeyCommand_Validate(t *testing.T) {
	t.Run("무기한(ExpiresAt=0) → 정상", func(t *testing.T) {
		c := IssueKeyCommand{}
		require.NoError(t, c.Validate(testNow))
	})
	t.Run("미래 만료 → 정상", func(t *testing.T) {
		c := IssueKeyCommand{ExpiresAt: testNow.Add(time.Hour)}
		require.NoError(t, c.Validate(testNow))
	})
	t.Run("과거 만료 → ErrValidation", func(t *testing.T) {
		c := IssueKeyCommand{ExpiresAt: testNow.Add(-time.Second)}
		err := c.Validate(testNow)
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "expires_at must be in the future", ve.Msg)
	})
	t.Run("현재(now == ExpiresAt) → ErrValidation(미래 아님)", func(t *testing.T) {
		c := IssueKeyCommand{ExpiresAt: testNow}
		err := c.Validate(testNow)
		var ve *ErrValidation
		require.ErrorAs(t, err, &ve)
	})
}

// TestErrValidation_Error 는 ErrValidation 이 error 로 동작함을 확인한다.
func TestErrValidation_Error(t *testing.T) {
	var err error = &ErrValidation{Msg: "boom"}
	assert.Equal(t, "boom", err.Error())
	var ve *ErrValidation
	assert.True(t, errors.As(err, &ve))
}
