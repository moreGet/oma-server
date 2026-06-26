package project

import (
	"errors"
	"strings"
	"testing"
)

func TestUpsertProjectCommand_Validate(t *testing.T) {
	var ve *ErrValidation
	if err := (UpsertProjectCommand{Name: "n"}).Validate(); !errors.As(err, &ve) {
		t.Errorf("missing client_id should fail validation, got %v", err)
	}
	if err := (UpsertProjectCommand{ClientID: "c"}).Validate(); !errors.As(err, &ve) {
		t.Errorf("missing name should fail validation, got %v", err)
	}
	if err := (UpsertProjectCommand{ClientID: "c", Name: "n"}).Validate(); err != nil {
		t.Errorf("valid command should pass, got %v", err)
	}
}

func TestUpsertProjectCommand_Normalize(t *testing.T) {
	c := UpsertProjectCommand{ClientID: "  c  ", Name: "  " + strings.Repeat("x", 300) + "  "}
	c.Normalize()
	if c.ClientID != "c" {
		t.Errorf("client_id not trimmed: %q", c.ClientID)
	}
	if len(c.Name) != maxNameLen {
		t.Errorf("name not truncated to %d: got %d", maxNameLen, len(c.Name))
	}
}

func TestUpsertConversationCommand_Validate(t *testing.T) {
	var ve *ErrValidation
	if err := (UpsertConversationCommand{Title: "t"}).Validate(); !errors.As(err, &ve) {
		t.Errorf("missing client_id should fail, got %v", err)
	}
	if err := (UpsertConversationCommand{ClientID: "c"}).Validate(); !errors.As(err, &ve) {
		t.Errorf("missing title should fail, got %v", err)
	}
	if err := (UpsertConversationCommand{ClientID: "c", Title: "t"}).Validate(); err != nil {
		t.Errorf("valid command should pass, got %v", err)
	}
}

func TestSettings_Validate(t *testing.T) {
	if err := (Settings{Backend: BackendDB}).Validate(); err != nil {
		t.Errorf("db backend should be valid, got %v", err)
	}
	if err := (Settings{Backend: BackendFile}).Validate(); err == nil {
		t.Error("file backend without dir should fail")
	}
	if err := (Settings{Backend: BackendFile, FileDir: "/x"}).Validate(); err != nil {
		t.Errorf("file backend with dir should pass, got %v", err)
	}
	if err := (Settings{Backend: BackendS3, S3Endpoint: "e", S3Bucket: "b"}).Validate(); err == nil {
		t.Error("s3 without access_key should fail")
	}
	if err := (Settings{Backend: BackendS3, S3Endpoint: "e", S3Bucket: "b", S3AccessKey: "k"}).Validate(); err != nil {
		t.Errorf("complete s3 should pass, got %v", err)
	}
	if err := (Settings{Backend: "weird"}).Validate(); err == nil {
		t.Error("unknown backend should fail")
	}
}

func TestSettings_NormalizeClampsNegativeMax(t *testing.T) {
	s := Settings{Backend: "", DefaultMaxSessions: -5}
	s.Normalize()
	if s.Backend != BackendDB {
		t.Errorf("empty backend should normalize to db, got %q", s.Backend)
	}
	if s.DefaultMaxSessions != 0 {
		t.Errorf("negative max should clamp to 0, got %d", s.DefaultMaxSessions)
	}
}

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if s.Backend != BackendDB || !s.S3UseSSL || s.DefaultMaxSessions != 0 {
		t.Errorf("unexpected defaults: %+v", s)
	}
}
