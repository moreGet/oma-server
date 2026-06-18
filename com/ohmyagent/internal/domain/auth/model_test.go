package auth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleLevel_CanControl(t *testing.T) {
	tests := []struct {
		name   string
		actor  RoleLevel
		target RoleLevel
		want   bool
	}{
		{"super over admin", RoleLevelSuperAdmin, RoleLevelAdmin, true},
		{"super over user", RoleLevelSuperAdmin, RoleLevelUser, true},
		{"admin over user", RoleLevelAdmin, RoleLevelUser, true},
		{"admin over admin (equal)", RoleLevelAdmin, RoleLevelAdmin, false},
		{"user over user (equal)", RoleLevelUser, RoleLevelUser, false},
		{"user over admin (lower)", RoleLevelUser, RoleLevelAdmin, false},
		{"admin over super (lower)", RoleLevelAdmin, RoleLevelSuperAdmin, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.actor.CanControl(tt.target))
		})
	}
}

func TestCreateMemberCommand_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cmd     CreateMemberCommand
		wantErr bool
		wantMsg string
	}{
		{
			name:    "valid",
			cmd:     CreateMemberCommand{Username: "alice", Password: "password1", RoleID: 1},
			wantErr: false,
		},
		{
			name:    "valid admin role",
			cmd:     CreateMemberCommand{Username: "bob", Password: "longenough", RoleID: 2},
			wantErr: false,
		},
		{
			name:    "empty username",
			cmd:     CreateMemberCommand{Username: "", Password: "password1", RoleID: 1},
			wantErr: true,
			wantMsg: "username is required",
		},
		{
			name:    "whitespace-only username (trimmed to empty)",
			cmd:     CreateMemberCommand{Username: "   ", Password: "password1", RoleID: 1},
			wantErr: true,
			wantMsg: "username is required",
		},
		{
			name:    "short password",
			cmd:     CreateMemberCommand{Username: "alice", Password: "short", RoleID: 1},
			wantErr: true,
			wantMsg: "password must be >= 8 chars",
		},
		{
			name:    "role_id too low",
			cmd:     CreateMemberCommand{Username: "alice", Password: "password1", RoleID: 0},
			wantErr: true,
			wantMsg: "invalid role_id",
		},
		{
			name:    "role_id too high",
			cmd:     CreateMemberCommand{Username: "alice", Password: "password1", RoleID: 4},
			wantErr: true,
			wantMsg: "invalid role_id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmd
			err := cmd.Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ve *ErrValidation
			require.True(t, errors.As(err, &ve), "expected *ErrValidation, got %T", err)
			assert.Equal(t, tt.wantMsg, ve.Msg)
		})
	}
}

func TestCreateMemberCommand_Normalize(t *testing.T) {
	cmd := CreateMemberCommand{Username: "  alice  ", Password: "password1", RoleID: 1}
	cmd.Normalize()
	assert.Equal(t, "alice", cmd.Username)
}
