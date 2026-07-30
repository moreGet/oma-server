package httpin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// defaultMessageHistoryLimit 은 메시지 이력/멘션 피드 조회 기본 페이지 크기다(미지정 시).
// 서비스/레포지토리에서 상한(최대 200)으로 다시 보정한다.
const defaultMessageHistoryLimit = 50

// messagingService 는 채팅 유스케이스 소비자 인터페이스다(*messagingapp.Service 가 충족).
type messagingService interface {
	ListRooms(ctx context.Context, actorID string) ([]domainmessaging.Room, error)
	CreateGroup(ctx context.Context, actorID, name string, memberIDs []string) (domainmessaging.Room, error)
	CreateDirect(ctx context.Context, actorID, otherID string) (domainmessaging.Room, error)
	History(ctx context.Context, actorID, roomID string, limit int, beforeID string) ([]domainmessaging.Message, error)
	SendMessage(ctx context.Context, actorID, roomID, content string, mentions []string, attachments []domainmessaging.Attachment) (domainmessaging.Message, error)
	MarkRead(ctx context.Context, actorID, roomID string) (int64, error)
	UnreadByRoom(ctx context.Context, actorID string) (map[string]int, error)
	ReadStates(ctx context.Context, actorID, roomID string) ([]domainmessaging.ReadState, error)
	Typing(ctx context.Context, actorID, roomID, state string) error
	RoomMembers(ctx context.Context, actorID, roomID string) ([]string, error)
	RoomMembersDetail(ctx context.Context, actorID, roomID string) ([]domainmessaging.MemberInfo, error)
	AddMembers(ctx context.Context, actorID, roomID string, memberIDs []string) ([]string, error)
	LeaveRoom(ctx context.Context, actorID, roomID string) error
	KickMember(ctx context.Context, actorID, roomID, targetID string) error
	EditMessage(ctx context.Context, actorID, roomID, messageID, content string) (domainmessaging.Message, error)
	DeleteMessage(ctx context.Context, actorID, roomID, messageID string) error
	RoomPresence(ctx context.Context, actorID, roomID string) ([]string, error)
	MentionsFeed(ctx context.Context, actorID string, limit int) ([]domainmessaging.Message, error)
	UploadAttachment(ctx context.Context, uploaderID, fileName, contentType string, data []byte) (domainmessaging.Attachment, error)
	DownloadAttachment(ctx context.Context, id string) (domainmessaging.StoredAttachment, io.ReadCloser, error)
	Connect(ctx context.Context, memberID string) *messagingapp.Client
	Disconnect(ctx context.Context, c *messagingapp.Client)
}

// MessagingHandler 는 채팅 방/메시지 REST 핸들러다(중첩 envelope, 소유권 스코프).
type MessagingHandler struct {
	svc messagingService
}

// NewMessagingHandler 는 MessagingHandler 를 생성한다.
func NewMessagingHandler(svc messagingService) *MessagingHandler {
	return &MessagingHandler{svc: svc}
}

// --- DTO ---

type roomDTO struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // group | direct
	Name        string `json:"name,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UnreadCount int    `json:"unread_count"`
}

type roomsResp struct {
	Rooms []roomDTO `json:"rooms"`
}

type createGroupReq struct {
	Name      string   `json:"name"`
	MemberIDs []string `json:"member_ids"`
}

type createDirectReq struct {
	UserID string `json:"user_id"`
}

type roomMessageDTO struct {
	ID        string `json:"id"`
	RoomID    string `json:"room_id"`
	SenderID  string `json:"sender_id"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	EditedAt  int64  `json:"edited_at,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
}

type messagesResp struct {
	Messages []roomMessageDTO `json:"messages"`
}

type sendMessageReq struct {
	Content     string                       `json:"content"`
	Mentions    []string                     `json:"mentions"`
	Attachments []domainmessaging.Attachment `json:"attachments"`
}

type editMessageReq struct {
	Content string `json:"content"`
}

type presenceResp struct {
	Online []string `json:"online"`
}

type unreadResp struct {
	Total int            `json:"total"`
	Rooms map[string]int `json:"rooms"` // roomID → 안읽음 수(>0 만)
}

type markReadResp struct {
	RoomID     string `json:"room_id"`
	LastReadAt int64  `json:"last_read_at"`
}

type readStateDTO struct {
	MemberID   string `json:"member_id"`
	LastReadAt int64  `json:"last_read_at"`
}

type readStatesResp struct {
	Reads []readStateDTO `json:"reads"`
}

type membersResp struct {
	Members []string `json:"members"`
}

// memberDetailDTO 는 ?detail=1 응답의 멤버 항목이다(UUID + 사람이 읽는 이름).
type memberDetailDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
}

type membersDetailResp struct {
	Members []memberDetailDTO `json:"members"`
}

type addMembersReq struct {
	MemberIDs []string `json:"member_ids"`
}

func toRoomDTO(r domainmessaging.Room) roomDTO {
	return roomDTO{ID: r.ID, Type: string(r.Type), Name: r.Name, CreatedAt: r.CreatedAt}
}

func toReadStateDTO(s domainmessaging.ReadState) readStateDTO {
	return readStateDTO{MemberID: s.MemberID, LastReadAt: s.LastReadAt}
}

func toMemberDetailDTO(mi domainmessaging.MemberInfo) memberDetailDTO {
	return memberDetailDTO{ID: mi.ID, Username: mi.Username, DisplayName: mi.DisplayName}
}

func toMessageDTO(m domainmessaging.Message) roomMessageDTO {
	return roomMessageDTO{
		ID: m.ID, RoomID: m.RoomID, SenderID: m.SenderID, Content: m.Content,
		CreatedAt: m.CreatedAt, EditedAt: m.EditedAt, Deleted: m.DeletedAt > 0,
	}
}

// --- Handlers ---

// ListRooms 는 GET /api/v1/chat/rooms — 본인이 속한 방 목록(방별 안읽음 수 포함).
func (h *MessagingHandler) ListRooms(w http.ResponseWriter, r *http.Request) error {
	actor := actorID(r)
	rooms, err := h.svc.ListRooms(r.Context(), actor)
	if err != nil {
		return messagingErr(err)
	}
	unread, err := h.svc.UnreadByRoom(r.Context(), actor)
	if err != nil {
		return messagingErr(err)
	}
	out := make([]roomDTO, 0, len(rooms))
	for _, rm := range rooms {
		d := toRoomDTO(rm)
		d.UnreadCount = unread[rm.ID]
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, roomsResp{Rooms: out})
	return nil
}

// Unread 는 GET /api/v1/chat/unread — 총/방별 안읽음 수(배지용).
func (h *MessagingHandler) Unread(w http.ResponseWriter, r *http.Request) error {
	byRoom, err := h.svc.UnreadByRoom(r.Context(), actorID(r))
	if err != nil {
		return messagingErr(err)
	}
	total := 0
	rooms := make(map[string]int, len(byRoom))
	for id, n := range byRoom {
		if n > 0 {
			rooms[id] = n
			total += n
		}
	}
	writeJSON(w, http.StatusOK, unreadResp{Total: total, Rooms: rooms})
	return nil
}

// MarkRead 는 POST /api/v1/chat/rooms/{id}/read — 방을 지금까지 읽음 처리.
func (h *MessagingHandler) MarkRead(w http.ResponseWriter, r *http.Request) error {
	readAt, err := h.svc.MarkRead(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, markReadResp{RoomID: r.PathValue("id"), LastReadAt: readAt})
	return nil
}

// ReadStates 는 GET /api/v1/chat/rooms/{id}/reads — 멤버별 읽음 위치(읽음 표시 렌더용).
func (h *MessagingHandler) ReadStates(w http.ResponseWriter, r *http.Request) error {
	states, err := h.svc.ReadStates(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, readStatesResp{Reads: mapSlice(states, toReadStateDTO)})
	return nil
}

// CreateGroup 은 POST /api/v1/chat/rooms — 단체 방 생성.
func (h *MessagingHandler) CreateGroup(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[createGroupReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	room, err := h.svc.CreateGroup(r.Context(), actorID(r), req.Name, req.MemberIDs)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusCreated, toRoomDTO(room))
	return nil
}

// CreateDirect 는 POST /api/v1/chat/rooms/direct — 1:1 방 가져오기/생성.
func (h *MessagingHandler) CreateDirect(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[createDirectReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	room, err := h.svc.CreateDirect(r.Context(), actorID(r), req.UserID)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, toRoomDTO(room))
	return nil
}

// History 는 GET /api/v1/chat/rooms/{id}/messages — 메시지 이력(최신순).
func (h *MessagingHandler) History(w http.ResponseWriter, r *http.Request) error {
	roomID := r.PathValue("id")
	limit := atoiDefault(r.URL.Query().Get("limit"), defaultMessageHistoryLimit)
	before := r.URL.Query().Get("before")
	msgs, err := h.svc.History(r.Context(), actorID(r), roomID, limit, before)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, messagesResp{Messages: mapSlice(msgs, toMessageDTO)})
	return nil
}

// SendMessage 는 POST /api/v1/chat/rooms/{id}/messages — REST 로 메시지 전송(WS 대안).
func (h *MessagingHandler) SendMessage(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[sendMessageReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	msg, err := h.svc.SendMessage(r.Context(), actorID(r), r.PathValue("id"), req.Content, req.Mentions, req.Attachments)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusCreated, toMessageDTO(msg))
	return nil
}

// EditMessage 는 PATCH /api/v1/chat/rooms/{id}/messages/{mid} — 본인 메시지 수정.
func (h *MessagingHandler) EditMessage(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[editMessageReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	msg, err := h.svc.EditMessage(r.Context(), actorID(r), r.PathValue("id"), r.PathValue("mid"), req.Content)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, toMessageDTO(msg))
	return nil
}

// DeleteMessage 는 DELETE /api/v1/chat/rooms/{id}/messages/{mid} — 본인 메시지 소프트 삭제.
func (h *MessagingHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.DeleteMessage(r.Context(), actorID(r), r.PathValue("id"), r.PathValue("mid")); err != nil {
		return messagingErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// RoomMembers 는 GET /api/v1/chat/rooms/{id}/members — 방 멤버 목록.
func (h *MessagingHandler) RoomMembers(w http.ResponseWriter, r *http.Request) error {
	roomID := r.PathValue("id")

	// ?detail=1 → 이름(username/display_name) 포함(방 멤버 누구나). 무인자는 기존 UUID 배열(하위호환).
	if r.URL.Query().Get("detail") == "1" {
		infos, err := h.svc.RoomMembersDetail(r.Context(), actorID(r), roomID)
		if err != nil {
			return messagingErr(err)
		}
		writeJSON(w, http.StatusOK, membersDetailResp{Members: mapSlice(infos, toMemberDetailDTO)})
		return nil
	}

	members, err := h.svc.RoomMembers(r.Context(), actorID(r), roomID)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, membersResp{Members: members})
	return nil
}

// AddMembers 는 POST /api/v1/chat/rooms/{id}/members — 단체 방에 멤버 추가.
func (h *MessagingHandler) AddMembers(w http.ResponseWriter, r *http.Request) error {
	req, err := bindJSON[addMembersReq](w, r, maxJSONBytes)
	if err != nil {
		return err
	}
	members, err := h.svc.AddMembers(r.Context(), actorID(r), r.PathValue("id"), req.MemberIDs)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, membersResp{Members: members})
	return nil
}

// Leave 는 POST /api/v1/chat/rooms/{id}/leave — 본인이 방에서 나가기.
func (h *MessagingHandler) Leave(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.LeaveRoom(r.Context(), actorID(r), r.PathValue("id")); err != nil {
		return messagingErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Kick 은 DELETE /api/v1/chat/rooms/{id}/members/{mid} — 방 생성자가 멤버 강퇴.
func (h *MessagingHandler) Kick(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.KickMember(r.Context(), actorID(r), r.PathValue("id"), r.PathValue("mid")); err != nil {
		return messagingErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Presence 는 GET /api/v1/chat/rooms/{id}/presence — 방 멤버 중 온라인 목록.
func (h *MessagingHandler) Presence(w http.ResponseWriter, r *http.Request) error {
	online, err := h.svc.RoomPresence(r.Context(), actorID(r), r.PathValue("id"))
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, presenceResp{Online: online})
	return nil
}

// Mentions 는 GET /api/v1/chat/mentions?limit= — 나를 멘션한 최신 메시지(알림 피드).
func (h *MessagingHandler) Mentions(w http.ResponseWriter, r *http.Request) error {
	limit := atoiDefault(r.URL.Query().Get("limit"), defaultMessageHistoryLimit)
	msgs, err := h.svc.MentionsFeed(r.Context(), actorID(r), limit)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusOK, messagesResp{Messages: mapSlice(msgs, toMessageDTO)})
	return nil
}

// multipartMemoryBudget 은 멀티파트 파싱이 **메모리에 유지할** 상한이다. 이보다 큰 파트는
// 임시 파일로 스필되어 RAM 을 점유하지 않는다.
//
// 주의: ParseMultipartForm 의 인자는 "허용 최대 크기"가 아니라 "메모리 한도"다. 여기에
// MaxAttachmentBytes 를 주면 스필이 아예 일어나지 않아 10MiB 파일이 통째로 RAM 에 남는다.
// 동시 업로드 수만큼 곱해지므로 100 건이면 GiB 단위가 된다 — 인증된 사용자면 누구나 칠 수 있다.
const multipartMemoryBudget = 1 << 20 // 1 MiB

// UploadAttachment 는 POST /api/v1/chat/attachments — multipart 파일 업로드 → 첨부 메타데이터(다운로드 URL).
func (h *MessagingHandler) UploadAttachment(w http.ResponseWriter, r *http.Request) error {
	// 본문 크기 상한(헤더/멀티파트 오버헤드 여유 1MiB).
	r.Body = http.MaxBytesReader(w, r.Body, domainmessaging.MaxAttachmentBytes+(1<<20))
	if err := r.ParseMultipartForm(multipartMemoryBudget); err != nil {
		return ErrBadRequest("file too large or invalid multipart form")
	}
	// 스필된 임시 파일을 핸들러 종료 시 즉시 정리한다(서버도 요청 종료 시 정리하지만 더 일찍 반납).
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		return ErrBadRequest("missing form file 'file'")
	}
	defer func() { _ = file.Close() }()

	// 크기를 먼저 보고 초과분은 읽기 전에 거부한다(10MiB 를 다 읽고 나서 버리지 않도록).
	if header.Size > domainmessaging.MaxAttachmentBytes {
		return messagingErr(domainmessaging.ErrAttachmentTooLarge)
	}
	// io.ReadAll 은 512B 에서 시작해 배로 늘려가며 재할당한다 — 10MiB 면 십수 회 복사에
	// 순간 최대 2배를 점유한다. 크기를 알고 있으므로 정확히 한 번만 할당한다.
	data := make([]byte, header.Size)
	if _, err := io.ReadFull(file, data); err != nil {
		return ErrBadRequest("failed to read file")
	}
	contentType := header.Header.Get("Content-Type")
	att, err := h.svc.UploadAttachment(r.Context(), actorID(r), header.Filename, contentType, data)
	if err != nil {
		return messagingErr(err)
	}
	writeJSON(w, http.StatusCreated, att)
	return nil
}

// DownloadAttachment 는 GET /api/v1/chat/attachments/{aid} — 첨부 바이너리 스트리밍(인증 필요).
func (h *MessagingHandler) DownloadAttachment(w http.ResponseWriter, r *http.Request) error {
	sa, body, err := h.svc.DownloadAttachment(r.Context(), r.PathValue("aid"))
	if err != nil {
		return messagingErr(err)
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", sa.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(sa.SizeBytes, 10))
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(sa.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// 헤더를 이미 보냈으므로 이 지점 이후의 실패는 상태코드로 바꿀 수 없다.
	// 응답은 Content-Length 보다 짧게 끊기고, 원인은 서버 로그로만 남긴다.
	if _, err := io.Copy(w, body); err != nil {
		slog.Error("attachment download interrupted", "event", "attachment.download_failed",
			"attachment_id", sa.ID, "size_bytes", sa.SizeBytes, "error", err)
	}
	return nil
}

// messagingErr 는 채팅 도메인 에러를 AppError 로 매핑한다.
func messagingErr(err error) error {
	switch {
	case errors.Is(err, domainmessaging.ErrRoomNotFound):
		return ErrNotFound("room not found")
	case errors.Is(err, domainmessaging.ErrMessageNotFound):
		return ErrNotFound("message not found")
	case errors.Is(err, domainmessaging.ErrAttachmentNotFound):
		return ErrNotFound("attachment not found")
	case errors.Is(err, domainmessaging.ErrAttachmentTooLarge):
		return ErrBadRequest(fmt.Sprintf("attachment exceeds max size (%d MiB)", domainmessaging.MaxAttachmentBytes>>20))
	case errors.Is(err, domainmessaging.ErrAttachmentEmpty):
		return ErrBadRequest("attachment is empty")
	case errors.Is(err, domainmessaging.ErrNotMember):
		return ErrForbidden("not a room member")
	case errors.Is(err, domainmessaging.ErrNotSender):
		return ErrForbidden("not the message owner")
	case errors.Is(err, domainmessaging.ErrNotRoomOwner):
		return ErrForbidden("not the room owner")
	case errors.Is(err, domainmessaging.ErrInvalidRoom):
		return ErrBadRequest("invalid room request")
	default:
		return err
	}
}
