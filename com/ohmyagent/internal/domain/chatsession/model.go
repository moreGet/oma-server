// Package chatsession 은 서버측 채팅 히스토리(세션) 동기화 어그리거트의 도메인 모델·포트를 담는다.
// 세션 본문(Data)은 클라이언트가 소유하는 불투명(opaque) JSON 이며, 서버는 소유권·시각만 관리한다.
// 도메인 규칙(스펙 §1): 외부 import 금지. stdlib(errors/strings/time)만 사용한다.
package chatsession

import (
	"errors"
	"strings"
	"time"
)

// --- 도메인 에러 ---
var (
	ErrNotFound   = errors.New("session not found") // → 404
	ErrPermission = errors.New("permission denied") // → 403
)

// ErrValidation 은 입력 검증 실패를 나타내는 typed 에러다. → 400
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

const maxTitleLen = 255

// Session 은 한 채팅 세션이다. Data 는 클라이언트 히스토리 JSON 원문(서버 불투명).
type Session struct {
	ID        string
	OwnerID   string
	Title     string
	Data      []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Summary 는 목록 조회용 요약이다(본문 Data 제외).
type Summary struct {
	ID        string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertCommand 는 세션 생성/갱신(upsert) 입력이다.
type UpsertCommand struct {
	ID      string // 경로의 세션 ID(클라이언트 생성)
	OwnerID string // actor
	Title   string
	Data    []byte // 불투명 히스토리 JSON
}

// Normalize 는 title 공백을 정리하고 길이를 제한한다.
func (c *UpsertCommand) Normalize() {
	c.Title = strings.TrimSpace(c.Title)
	if len(c.Title) > maxTitleLen {
		c.Title = c.Title[:maxTitleLen]
	}
}

// Validate 는 upsert 입력을 검증한다.
func (c *UpsertCommand) Validate() error {
	c.Normalize()
	if c.ID == "" {
		return &ErrValidation{Msg: "session id is required"}
	}
	if len(c.Data) == 0 {
		return &ErrValidation{Msg: "data is required"}
	}
	return nil
}
