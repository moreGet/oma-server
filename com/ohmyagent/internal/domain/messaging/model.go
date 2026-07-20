// Package messaging 는 사용자 간 실시간 채팅(단체/1:1)의 도메인 모델·포트를 담는다.
// LLM chat(POST /chat)과는 무관한 사람↔사람 메시징이다. 메시지는 RDB 에 영속화한다.
package messaging

import (
	"context"
	"errors"
	"io"
)

// RoomType 은 방 종류다.
type RoomType string

const (
	RoomGroup  RoomType = "group"  // 단체 채팅(N명)
	RoomDirect RoomType = "direct" // 1:1 채팅(2명, 정준 키로 중복 방지)
)

// 도메인 에러(핸들러가 HTTP 상태로 매핑).
var (
	ErrRoomNotFound    = errors.New("room not found")
	ErrNotMember       = errors.New("not a room member")     // 방 멤버 아님 → 403
	ErrInvalidRoom     = errors.New("invalid room")          // 멤버 부족/자기 자신과 direct 등
	ErrMessageNotFound = errors.New("message not found")     // 메시지 없음/삭제됨 → 404
	ErrNotSender       = errors.New("not the message owner") // 본인 메시지 아님 → 403
	ErrNotRoomOwner    = errors.New("not the room owner")    // 방 생성자 아님(강퇴 권한) → 403

	ErrAttachmentNotFound = errors.New("attachment not found")        // → 404
	ErrAttachmentTooLarge = errors.New("attachment exceeds max size") // → 400
	ErrAttachmentEmpty    = errors.New("attachment is empty")         // → 400
)

// MaxAttachmentBytes 는 첨부 파일 1건의 최대 크기(10 MiB)다.
const MaxAttachmentBytes = 10 << 20

// Room 은 채팅방이다.
type Room struct {
	ID        string
	Type      RoomType
	Name      string // group 표시명(direct 는 빈 값)
	DirectKey string // direct 정준 키(group 은 빈 값)
	CreatedBy string
	CreatedAt int64 // unix sec
}

// Attachment 는 메시지 첨부 파일의 메타데이터다(바이너리는 url 이 가리키는 스토리지에서 다운로드).
type Attachment struct {
	ID          string `json:"id,omitempty"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	URL         string `json:"url"`
}

// StoredAttachment 는 저장된 첨부 파일의 메타데이터다(바이너리는 별도).
type StoredAttachment struct {
	ID          string
	FileName    string
	ContentType string
	SizeBytes   int64
	UploaderID  string
	CreatedAt   int64
}

// AttachmentStore — 첨부 바이너리 영속화 포트(향후 파일/S3 백엔드로 교체 가능한 경계).
type AttachmentStore interface {
	// Put 은 첨부 메타데이터 + 바이너리를 저장한다.
	Put(ctx context.Context, a StoredAttachment, data []byte) error
	// Open 은 첨부 메타데이터와 바이너리 **스트림**을 반환한다(없으면 ErrAttachmentNotFound).
	// 호출자가 반드시 Close 해야 한다.
	//
	// 바이너리를 []byte 로 한 번에 돌려주지 않는 이유: 파일 크기 × 동시 다운로드 수만큼
	// 메모리가 필요해져 대용량 첨부에서 OOM 으로 이어진다. 구현은 크기와 무관하게
	// 상수 메모리로 읽어야 한다.
	Open(ctx context.Context, id string) (StoredAttachment, io.ReadCloser, error)
	// Stats 는 첨부 개수와 총 바이트 수를 반환한다(어드민 집계).
	Stats(ctx context.Context) (count int, totalBytes int64, err error)
}

// Message 는 한 건의 채팅 메시지다.
type Message struct {
	ID          string
	RoomID      string
	SenderID    string
	Content     string
	CreatedAt   int64        // unix sec
	EditedAt    int64        // unix sec, 0 = 수정 안 함
	DeletedAt   int64        // unix sec, 0 = 삭제 안 함(소프트 삭제 시 content 비움)
	Mentions    []string     // 멘션된 멤버 ID(방 멤버로 검증됨)
	Attachments []Attachment // 첨부 파일 메타데이터
}

// ReadState 는 한 멤버가 방을 어디까지 읽었는지다(읽음 표시용).
type ReadState struct {
	MemberID   string
	LastReadAt int64 // unix sec, 0 = 안 읽음
}

// AdminStats 는 어드민 채팅 대시보드 집계다.
type AdminStats struct {
	Rooms           int
	GroupRooms      int
	DirectRooms     int
	Messages        int // 삭제 제외 활성 메시지
	DeletedMessages int
	Attachments     int
	AttachmentBytes int64
}

// AdminRoom 은 어드민 방 목록의 한 행이다(방 + 집계).
type AdminRoom struct {
	Room
	MemberCount  int
	MessageCount int
	LastActivity int64 // 마지막 메시지 시각(없으면 생성 시각)
}

// DirectKey 는 두 멤버의 1:1 방 정준 키(작은ID:큰ID)를 만든다(순서 무관 동일 키).
func DirectKey(a, b string) string {
	if a <= b {
		return a + ":" + b
	}
	return b + ":" + a
}

// RoomRepository — 방/멤버십 영속화 포트.
type RoomRepository interface {
	// Create 는 방과 멤버를 원자적으로 생성한다.
	Create(ctx context.Context, room Room, memberIDs []string) error
	// Get 은 방을 반환한다(없으면 ErrRoomNotFound).
	Get(ctx context.Context, roomID string) (Room, error)
	// FindDirect 는 정준 키로 기존 1:1 방을 찾는다(없으면 ErrRoomNotFound).
	FindDirect(ctx context.Context, directKey string) (Room, error)
	// ListForMember 는 멤버가 속한 방 목록을 최근 활동순으로 반환한다.
	ListForMember(ctx context.Context, memberID string) ([]Room, error)
	// Members 는 방의 멤버 ID 목록을 반환한다.
	Members(ctx context.Context, roomID string) ([]string, error)
	// IsMember 는 멤버가 방에 속하는지 반환한다.
	IsMember(ctx context.Context, roomID, memberID string) (bool, error)
	// MarkRead 는 멤버의 방 읽음 위치를 readAt 로 전진시킨다(뒤로 가지 않음).
	MarkRead(ctx context.Context, roomID, memberID string, readAt int64) error
	// UnreadByRoom 은 멤버의 방별 안읽음 수(내 last_read_at 이후 남이 보낸 메시지)를 한 번에 반환한다.
	UnreadByRoom(ctx context.Context, memberID string) (map[string]int, error)
	// ReadStates 는 방의 멤버별 읽음 위치를 반환한다(읽음 표시 렌더용).
	ReadStates(ctx context.Context, roomID string) ([]ReadState, error)
	// AddMembers 는 멤버를 방에 추가한다(이미 멤버면 무시 — idempotent). last_read_at 은 joinedAt 으로 시작(가입 전 메시지는 안읽음 제외).
	AddMembers(ctx context.Context, roomID string, memberIDs []string, joinedAt int64) error
	// RemoveMember 는 멤버를 방에서 제거한다(나가기/강퇴).
	RemoveMember(ctx context.Context, roomID, memberID string) error
	// CoMembers 는 member 와 한 방이라도 공유하는 모든 멤버 ID(본인 포함)를 반환한다(presence 전파 대상).
	CoMembers(ctx context.Context, memberID string) ([]string, error)

	// --- 어드민(전체 조회/관리) ---
	// Stats 는 방/메시지 집계를 반환한다(첨부는 AttachmentStore.Stats 로 별도).
	Stats(ctx context.Context) (AdminStats, error)
	// ListAllRooms 는 전체 방을 최근 활동순으로(집계 포함) 반환한다.
	ListAllRooms(ctx context.Context, limit int) ([]AdminRoom, error)
	// DeleteRoom 은 방 + 멤버십 + 메시지를 삭제한다.
	DeleteRoom(ctx context.Context, roomID string) error
}

// MemberInfo 는 채팅 표시용 멤버 이름 정보다(UUID → 사람이 읽는 이름 해석 결과).
// DisplayName 이 비면 클라이언트가 Username 으로 폴백한다(둘 중 하나는 채워진다).
type MemberInfo struct {
	ID          string
	Username    string
	DisplayName string
}

// MemberDirectory — out 포트(멤버 ID → 표시 이름 해석). auth 도메인 직접 의존을 피하기 위한 경계.
// 채팅 멤버 이름 표시(멤버 목록/멘션/1:1 상대)에 사용한다. 멤버십 스코프는 호출하는 유스케이스가 보장한다.
type MemberDirectory interface {
	// NamesByIDs 는 주어진 멤버 ID 들의 이름 정보를 id→MemberInfo 맵으로 반환한다(없는 id 는 제외).
	NamesByIDs(ctx context.Context, ids []string) (map[string]MemberInfo, error)
}

// MessageRepository — 메시지 영속화 포트(향후 NoSQL 등으로 교체 가능한 경계).
type MessageRepository interface {
	// Save 는 메시지를 저장한다.
	Save(ctx context.Context, m Message) error
	// List 는 방의 메시지를 최신순으로 반환한다(beforeID 가 있으면 그 이전 페이지, limit 개).
	// 삭제된 메시지도 순서 유지를 위해 포함하되 content 는 비어 있고 DeletedAt>0 으로 표시한다.
	List(ctx context.Context, roomID string, limit int, beforeID string) ([]Message, error)
	// GetMessage 는 메시지 한 건을 반환한다(없으면 ErrMessageNotFound).
	GetMessage(ctx context.Context, messageID string) (Message, error)
	// UpdateContent 는 메시지 본문을 수정하고 editedAt 을 기록한다.
	UpdateContent(ctx context.Context, messageID, content string, editedAt int64) error
	// MarkDeleted 는 메시지를 소프트 삭제한다(deletedAt 기록 + content 비움).
	MarkDeleted(ctx context.Context, messageID string, deletedAt int64) error
	// Mentioning 은 memberID 가 멘션된 최신 메시지(삭제 제외)를 limit 개 반환한다(알림 피드).
	Mentioning(ctx context.Context, memberID string, limit int) ([]Message, error)
}
