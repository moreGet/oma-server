// Package project 는 클라이언트 동기화용 프로젝트/대화(세션) 어그리거트의 도메인 모델·포트를 담는다.
// 메타데이터는 DB, 대화 본문(messages)은 선택형 백엔드(DB/파일/S3)에 디렉터리 구조로 저장한다.
package project

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrValidation 은 입력 검증 실패다(→400).
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// ErrSessionLimitExceeded 는 계정별 최대 세션 수 초과다(하드 캡 → 429).
var ErrSessionLimitExceeded = errors.New("session storage limit exceeded")

// ErrNotFound 는 소유자 범위에서 대상을 못 찾음이다(→404).
var ErrNotFound = errors.New("not found")

const maxNameLen = 255

// Project 는 대화 세션을 묶는 상위 컨테이너다.
type Project struct {
	ID                string
	OwnerID           string
	ClientID          string // 클라 로컬 GUID(업서트 키)
	Name              string
	CreatedUTC        time.Time
	UpdatedUTC        time.Time
	ConversationCount int
}

// Conversation 은 한 대화 세션의 메타데이터다(본문은 ContentStore 에 별도 저장).
type Conversation struct {
	ID           string
	ProjectID    string // "" = 미분류
	OwnerID      string
	ClientID     string // 클라 세션 GUID(업서트 키)
	Title        string
	CreatedUTC   time.Time
	UpdatedUTC   time.Time
	MessageCount int
}

// UpsertProjectCommand 는 프로젝트 생성/업서트 입력이다.
type UpsertProjectCommand struct {
	OwnerID  string
	ClientID string
	Name     string
}

func (c *UpsertProjectCommand) Normalize() {
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.Name = strings.TrimSpace(c.Name)
	if len(c.Name) > maxNameLen {
		c.Name = c.Name[:maxNameLen]
	}
}

func (c UpsertProjectCommand) Validate() error {
	if c.ClientID == "" {
		return &ErrValidation{Msg: "client_id is required"}
	}
	if c.Name == "" {
		return &ErrValidation{Msg: "name is required"}
	}
	return nil
}

// UpsertConversationCommand 는 대화 업서트(push) 입력이다. Messages 는 불투명 JSON(기존 채팅 messages 스키마).
type UpsertConversationCommand struct {
	OwnerID      string
	ProjectID    string
	ClientID     string
	Title        string
	CreatedUTC   time.Time
	UpdatedUTC   time.Time
	Messages     []byte // 원문 JSON 배열(ContentStore 에 저장)
	MessageCount int
}

func (c *UpsertConversationCommand) Normalize() {
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.Title = strings.TrimSpace(c.Title)
	if len(c.Title) > maxNameLen {
		c.Title = c.Title[:maxNameLen]
	}
}

func (c UpsertConversationCommand) Validate() error {
	if c.ClientID == "" {
		return &ErrValidation{Msg: "client_id is required"}
	}
	if c.Title == "" {
		return &ErrValidation{Msg: "title is required"}
	}
	return nil
}

// --- 포트 ---

// ProjectRepository — 프로젝트 메타데이터 영속화.
type ProjectRepository interface {
	UpsertProject(ctx context.Context, p Project) (Project, error) // owner+client_id 업서트, 서버 id 반환
	ListProjects(ctx context.Context, ownerID string) ([]Project, error)
	GetProject(ctx context.Context, ownerID, id string) (Project, error)
	DeleteProject(ctx context.Context, ownerID, id string) error
}

// ConversationRepository — 대화 메타데이터 영속화.
type ConversationRepository interface {
	UpsertConversation(ctx context.Context, c Conversation) (Conversation, error)
	ListByProject(ctx context.Context, ownerID, projectID string) ([]Conversation, error)
	CountByOwner(ctx context.Context, ownerID string) (int, error)
	// FindIDByClient 는 owner+client_id 의 기존 서버 id 를 반환한다(없으면 "", false). 캡 판정·업서트용.
	FindIDByClient(ctx context.Context, ownerID, clientID string) (string, bool, error)
	DeleteConversation(ctx context.Context, ownerID, id string) error
}

// ContentStore — 대화 본문(messages) 블롭 저장(쓰기 전용). key 예: "owner/project/conv.json.gz".
type ContentStore interface {
	Save(ctx context.Context, key string, data []byte) error
}

// StoreFactory 는 Settings 로부터 ContentStore 를 만든다.
type StoreFactory interface {
	Build(s Settings) (ContentStore, error)
}

// Cipher 는 S3 시크릿 암복호화 포트다(provider cipher 재사용).
type Cipher interface {
	Encrypt(plain string) (string, error)
	Decrypt(enc string) (string, error)
}
