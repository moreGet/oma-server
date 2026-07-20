// Package messagingapp 는 사용자 간 채팅(단체/1:1) 유스케이스 + 인메모리 브로드캐스트 허브를 담는다.
package messagingapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// Service 는 채팅 방/메시지 유스케이스다. 멤버십을 강제하고, 전송 시 broadcaster 로 팬아웃한다.
// hub 는 이 인스턴스의 로컬 연결(등록/해제/온라인 조회)을, bc 는 이벤트 전파(memory/redis)를 담당한다.
type Service struct {
	rooms       domainmessaging.RoomRepository
	messages    domainmessaging.MessageRepository
	attachments domainmessaging.AttachmentStore
	directory   domainmessaging.MemberDirectory
	hub         *Hub
	bc          Broadcaster
	now         func() time.Time
	newID       func() string
	// typingMembers 는 타이핑 전파 전용 멤버 목록 캐시다(고빈도 신호의 DB 부하 제거).
	// 권한이 걸린 경로는 이 캐시를 쓰지 않는다 — typing_cache.go 주석 참고.
	typingMembers *memberListCache
}

// NewService 는 Service 를 생성한다. bc 가 nil 이면 단일 인스턴스(LocalBroadcaster)로 기본 동작한다.
// 멤버 이름 디렉터리는 기본 no-op(이름 미해석)이며, 조립 루트가 SetMemberDirectory 로 주입한다.
func NewService(rooms domainmessaging.RoomRepository, messages domainmessaging.MessageRepository, attachments domainmessaging.AttachmentStore, hub *Hub, bc Broadcaster) *Service {
	if bc == nil {
		bc = NewLocalBroadcaster(hub)
	}
	return &Service{
		rooms: rooms, messages: messages, attachments: attachments, hub: hub, bc: bc,
		directory:     noopMemberDirectory{},
		now:           time.Now,
		newID:         func() string { return uuid.NewString() },
		typingMembers: newMemberListCache(time.Now),
	}
}

// SetMemberDirectory 는 멤버 이름 해석 디렉터리를 주입한다(조립 루트 전용, 기동 시 1회).
func (s *Service) SetMemberDirectory(d domainmessaging.MemberDirectory) {
	if d != nil {
		s.directory = d
	}
}

// noopMemberDirectory 는 이름을 해석하지 않는 기본 구현이다(디렉터리 미주입 시 폴백).
type noopMemberDirectory struct{}

func (noopMemberDirectory) NamesByIDs(context.Context, []string) (map[string]domainmessaging.MemberInfo, error) {
	return map[string]domainmessaging.MemberInfo{}, nil
}

// CreateGroup 은 단체 방을 만든다(생성자 포함, 멤버 중복 제거). name 필수.
func (s *Service) CreateGroup(ctx context.Context, actorID, name string, memberIDs []string) (domainmessaging.Room, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domainmessaging.Room{}, domainmessaging.ErrInvalidRoom
	}
	members := dedupe(append([]string{actorID}, memberIDs...))
	if len(members) < 1 {
		return domainmessaging.Room{}, domainmessaging.ErrInvalidRoom
	}
	room := domainmessaging.Room{
		ID: s.newID(), Type: domainmessaging.RoomGroup, Name: name,
		CreatedBy: actorID, CreatedAt: s.now().UTC().Unix(),
	}
	if err := s.rooms.Create(ctx, room, members); err != nil {
		return domainmessaging.Room{}, err
	}
	return room, nil
}

// CreateDirect 는 두 사용자의 1:1 방을 가져오거나(있으면) 새로 만든다. 자기 자신과는 불가.
func (s *Service) CreateDirect(ctx context.Context, actorID, otherID string) (domainmessaging.Room, error) {
	otherID = strings.TrimSpace(otherID)
	if otherID == "" || otherID == actorID {
		return domainmessaging.Room{}, domainmessaging.ErrInvalidRoom
	}
	key := domainmessaging.DirectKey(actorID, otherID)
	if existing, err := s.rooms.FindDirect(ctx, key); err == nil {
		return existing, nil
	} else if !errors.Is(err, domainmessaging.ErrRoomNotFound) {
		return domainmessaging.Room{}, err
	}
	room := domainmessaging.Room{
		ID: s.newID(), Type: domainmessaging.RoomDirect, DirectKey: key,
		CreatedBy: actorID, CreatedAt: s.now().UTC().Unix(),
	}
	if err := s.rooms.Create(ctx, room, []string{actorID, otherID}); err != nil {
		// 동시 생성 레이스: unique(direct_key) 충돌이면 기존 방을 재조회해 반환.
		if existing, ferr := s.rooms.FindDirect(ctx, key); ferr == nil {
			return existing, nil
		}
		return domainmessaging.Room{}, err
	}
	return room, nil
}

// ListRooms 는 actor 가 속한 방 목록을 반환한다.
func (s *Service) ListRooms(ctx context.Context, actorID string) ([]domainmessaging.Room, error) {
	return s.rooms.ListForMember(ctx, actorID)
}

// History 는 방의 메시지 이력을 반환한다(멤버만).
func (s *Service) History(ctx context.Context, actorID, roomID string, limit int, beforeID string) ([]domainmessaging.Message, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	return s.messages.List(ctx, roomID, limit, beforeID)
}

// SendMessage 는 메시지를 저장하고 방 멤버 전원에게 브로드캐스트한다(발신자 포함). 멤버만 가능.
// mentions 는 방 멤버로 검증(외부/비멤버는 제거), attachments 는 메타데이터로 저장한다.
func (s *Service) SendMessage(ctx context.Context, actorID, roomID, content string, mentions []string, attachments []domainmessaging.Attachment) (domainmessaging.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" && len(attachments) == 0 {
		return domainmessaging.Message{}, domainmessaging.ErrInvalidRoom // 텍스트도 첨부도 없음
	}
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return domainmessaging.Message{}, err
	}
	members, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return domainmessaging.Message{}, err
	}
	msg := domainmessaging.Message{
		ID: s.newID(), RoomID: roomID, SenderID: actorID,
		Content: content, CreatedAt: s.now().UTC().Unix(),
		Mentions: filterToMembers(dedupe(mentions), members), Attachments: attachments,
	}
	if err := s.messages.Save(ctx, msg); err != nil {
		return domainmessaging.Message{}, err
	}
	payload, err := json.Marshal(outboundEvent{Type: "message", Message: msgToDTO(msg)})
	if err == nil {
		s.bc.Broadcast(members, payload)
	}
	return msg, nil
}

// --- 온라인 상태(presence) ---

const wsSendBuffer = 64

// Connect 는 멤버의 WS 연결을 등록하고 첫 연결이면 co-member 들에게 online presence 를 알린다.
func (s *Service) Connect(ctx context.Context, memberID string) *Client {
	c := &Client{MemberID: memberID, Send: make(chan []byte, wsSendBuffer)}
	if s.hub.Register(c) {
		s.broadcastPresence(ctx, memberID, true)
	}
	return c
}

// Disconnect 는 연결을 해제하고 마지막 연결이면 offline presence 를 알린다.
func (s *Service) Disconnect(ctx context.Context, c *Client) {
	if s.hub.Unregister(c) {
		s.broadcastPresence(ctx, c.MemberID, false)
	}
}

func (s *Service) broadcastPresence(ctx context.Context, memberID string, online bool) {
	targets, err := s.rooms.CoMembers(ctx, memberID)
	if err != nil || len(targets) == 0 {
		return
	}
	if payload, mErr := json.Marshal(outboundEvent{Type: "presence", Presence: &presenceDTO{MemberID: memberID, Online: online}}); mErr == nil {
		s.bc.Broadcast(targets, payload)
	}
}

// RoomPresence 는 방 멤버 중 현재 온라인인 멤버 ID 를 반환한다(멤버만).
func (s *Service) RoomPresence(ctx context.Context, actorID, roomID string) ([]string, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	members, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return nil, err
	}
	return s.hub.OnlineAmong(members), nil
}

// --- 강퇴(creator 권한) ---

// KickMember 는 방 생성자가 다른 멤버를 강퇴한다(group 한정). 강퇴 대상 포함 방 멤버에게 member_left 를 알린다.
func (s *Service) KickMember(ctx context.Context, actorID, roomID, targetID string) error {
	if targetID == "" || targetID == actorID {
		return domainmessaging.ErrInvalidRoom // 본인은 leave 사용
	}
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		return err
	}
	if room.Type != domainmessaging.RoomGroup {
		return domainmessaging.ErrInvalidRoom
	}
	if room.CreatedBy != actorID {
		return domainmessaging.ErrNotRoomOwner // 생성자만 강퇴 가능
	}
	ok, err := s.rooms.IsMember(ctx, roomID, targetID)
	if err != nil {
		return err
	}
	if !ok {
		return domainmessaging.ErrNotMember // 대상이 방 멤버 아님
	}
	members, err := s.rooms.Members(ctx, roomID) // 제거 전(대상 포함) — 대상도 통지받아 방을 닫음
	if err != nil {
		return err
	}
	if err := s.rooms.RemoveMember(ctx, roomID, targetID); err != nil {
		return err
	}
	s.typingMembers.invalidate(roomID) // 강퇴 즉시 반영(옛 목록으로 전파되지 않도록)
	s.broadcastMember(members, "member_left", roomID, targetID)
	return nil
}

// --- 파일 첨부 업로드/다운로드 ---

// UploadAttachment 는 파일 바이너리를 저장하고 메시지에 실을 Attachment 메타데이터(다운로드 URL 포함)를 반환한다.
func (s *Service) UploadAttachment(ctx context.Context, uploaderID, fileName, contentType string, data []byte) (domainmessaging.Attachment, error) {
	if len(data) == 0 {
		return domainmessaging.Attachment{}, domainmessaging.ErrAttachmentEmpty
	}
	if len(data) > domainmessaging.MaxAttachmentBytes {
		return domainmessaging.Attachment{}, domainmessaging.ErrAttachmentTooLarge
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "file"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	id := s.newID()
	sa := domainmessaging.StoredAttachment{
		ID: id, FileName: fileName, ContentType: contentType,
		SizeBytes: int64(len(data)), UploaderID: uploaderID, CreatedAt: s.now().UTC().Unix(),
	}
	if err := s.attachments.Put(ctx, sa, data); err != nil {
		return domainmessaging.Attachment{}, err
	}
	return domainmessaging.Attachment{
		ID: id, FileName: fileName, ContentType: contentType, SizeBytes: int64(len(data)),
		URL: "/api/v1/chat/attachments/" + id,
	}, nil
}

// DownloadAttachment 는 저장된 첨부의 메타데이터와 바이너리 스트림을 반환한다.
// 호출자가 반드시 Close 해야 한다(스트림은 크기와 무관하게 상수 메모리로 읽힌다).
func (s *Service) DownloadAttachment(ctx context.Context, id string) (domainmessaging.StoredAttachment, io.ReadCloser, error) {
	return s.attachments.Open(ctx, id)
}

// --- 어드민(전체 조회/모더레이션) — 인가는 호출하는 web 어드민 레이어(CanManage)에서 강제 ---

// AdminStats 는 채팅 집계(방/메시지/첨부)를 반환한다.
func (s *Service) AdminStats(ctx context.Context) (domainmessaging.AdminStats, error) {
	st, err := s.rooms.Stats(ctx)
	if err != nil {
		return domainmessaging.AdminStats{}, err
	}
	count, bytes, err := s.attachments.Stats(ctx)
	if err != nil {
		return domainmessaging.AdminStats{}, err
	}
	st.Attachments, st.AttachmentBytes = count, bytes
	return st, nil
}

// AdminListRooms 는 전체 방을 최근 활동순(집계 포함)으로 반환한다.
func (s *Service) AdminListRooms(ctx context.Context, limit int) ([]domainmessaging.AdminRoom, error) {
	return s.rooms.ListAllRooms(ctx, limit)
}

// AdminRoomDetail 은 방 + 멤버 + 최근 메시지를 반환한다(모더레이션 뷰).
func (s *Service) AdminRoomDetail(ctx context.Context, roomID string) (domainmessaging.Room, []string, []domainmessaging.Message, error) {
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		return domainmessaging.Room{}, nil, nil, err
	}
	members, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return domainmessaging.Room{}, nil, nil, err
	}
	msgs, err := s.messages.List(ctx, roomID, 100, "")
	if err != nil {
		return domainmessaging.Room{}, nil, nil, err
	}
	return room, members, msgs, nil
}

// AdminDeleteMessage 는 메시지를 소프트 삭제하고 방 멤버에게 message_deleted 를 브로드캐스트한다(모더레이션).
func (s *Service) AdminDeleteMessage(ctx context.Context, messageID string) error {
	msg, err := s.messages.GetMessage(ctx, messageID)
	if err != nil {
		return err
	}
	if msg.DeletedAt > 0 {
		return nil
	}
	deletedAt := s.now().UTC().Unix()
	if err := s.messages.MarkDeleted(ctx, messageID, deletedAt); err != nil {
		return err
	}
	msg.Content, msg.DeletedAt = "", deletedAt
	s.broadcastMessageEvent(ctx, "message_deleted", msg)
	return nil
}

// AdminDeleteRoom 은 방 전체를 삭제한다(방+멤버+메시지).
func (s *Service) AdminDeleteRoom(ctx context.Context, roomID string) error {
	return s.rooms.DeleteRoom(ctx, roomID)
}

// --- 멘션 피드 ---

// MentionsFeed 는 actor 가 멘션된 최신 메시지(삭제 제외)를 반환한다.
func (s *Service) MentionsFeed(ctx context.Context, actorID string, limit int) ([]domainmessaging.Message, error) {
	return s.messages.Mentioning(ctx, actorID, limit)
}

// filterToMembers 는 ids 중 members 에 속한 것만 남긴다(멘션 검증).
func filterToMembers(ids, members []string) []string {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(members))
	for _, m := range members {
		set[m] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EditMessage 는 본인 메시지의 본문을 수정한다(멤버·발신자 본인만). 수정 후 message_edited 를 브로드캐스트.
func (s *Service) EditMessage(ctx context.Context, actorID, roomID, messageID, content string) (domainmessaging.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return domainmessaging.Message{}, domainmessaging.ErrInvalidRoom
	}
	msg, err := s.ownMessage(ctx, actorID, roomID, messageID)
	if err != nil {
		return domainmessaging.Message{}, err
	}
	if msg.DeletedAt > 0 {
		return domainmessaging.Message{}, domainmessaging.ErrMessageNotFound // 삭제된 메시지는 수정 불가
	}
	editedAt := s.now().UTC().Unix()
	if err := s.messages.UpdateContent(ctx, messageID, content, editedAt); err != nil {
		return domainmessaging.Message{}, err
	}
	msg.Content, msg.EditedAt = content, editedAt
	s.broadcastMessageEvent(ctx, "message_edited", msg)
	return msg, nil
}

// DeleteMessage 는 본인 메시지를 소프트 삭제한다(멤버·발신자 본인만). 삭제 후 message_deleted 를 브로드캐스트.
func (s *Service) DeleteMessage(ctx context.Context, actorID, roomID, messageID string) error {
	msg, err := s.ownMessage(ctx, actorID, roomID, messageID)
	if err != nil {
		return err
	}
	if msg.DeletedAt > 0 {
		return nil // 이미 삭제됨 — idempotent
	}
	deletedAt := s.now().UTC().Unix()
	if err := s.messages.MarkDeleted(ctx, messageID, deletedAt); err != nil {
		return err
	}
	msg.Content, msg.DeletedAt = "", deletedAt
	s.broadcastMessageEvent(ctx, "message_deleted", msg)
	return nil
}

// ownMessage 는 메시지를 조회하고 (방 일치 + 멤버 + 발신자 본인)을 검증한다.
func (s *Service) ownMessage(ctx context.Context, actorID, roomID, messageID string) (domainmessaging.Message, error) {
	msg, err := s.messages.GetMessage(ctx, messageID)
	if err != nil {
		return domainmessaging.Message{}, err
	}
	if msg.RoomID != roomID {
		return domainmessaging.Message{}, domainmessaging.ErrMessageNotFound
	}
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return domainmessaging.Message{}, err
	}
	if msg.SenderID != actorID {
		return domainmessaging.Message{}, domainmessaging.ErrNotSender
	}
	return msg, nil
}

// broadcastMessageEvent 는 메시지 수정/삭제 이벤트를 방 멤버에게 전송한다.
func (s *Service) broadcastMessageEvent(ctx context.Context, eventType string, msg domainmessaging.Message) {
	members, err := s.rooms.Members(ctx, msg.RoomID)
	if err != nil || len(members) == 0 {
		return
	}
	if payload, mErr := json.Marshal(outboundEvent{Type: eventType, Message: msgToDTO(msg)}); mErr == nil {
		s.bc.Broadcast(members, payload)
	}
}

// MarkRead 는 방을 "지금까지" 읽음 처리하고 방 멤버에게 read 이벤트를 브로드캐스트한다(멤버만).
func (s *Service) MarkRead(ctx context.Context, actorID, roomID string) (int64, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return 0, err
	}
	readAt := s.now().UTC().Unix()
	if err := s.rooms.MarkRead(ctx, roomID, actorID, readAt); err != nil {
		return 0, err
	}
	if members, err := s.rooms.Members(ctx, roomID); err == nil && len(members) > 0 {
		if payload, mErr := json.Marshal(outboundEvent{
			Type: "read",
			Read: &readDTO{RoomID: roomID, MemberID: actorID, LastReadAt: readAt},
		}); mErr == nil {
			s.bc.Broadcast(members, payload)
		}
	}
	return readAt, nil
}

// UnreadByRoom 은 actor 의 방별 안읽음 수를 반환한다.
func (s *Service) UnreadByRoom(ctx context.Context, actorID string) (map[string]int, error) {
	return s.rooms.UnreadByRoom(ctx, actorID)
}

// Typing 은 타이핑 상태(start|stop)를 방의 **다른** 멤버에게 중계한다(멤버만, 저장 안 함 — 휘발성).
//
// 키 입력마다 오는 고빈도 신호라 DB 왕복을 최소화한다: 멤버 목록을 한 번만 얻어
// 멤버십 검사와 수신자 추출을 같은 순회에서 끝낸다(예전에는 IsMember + Members 로 2회).
// 목록은 짧은 TTL 캐시를 거치므로 정상 흐름에서는 쿼리가 아예 나가지 않는다.
func (s *Service) Typing(ctx context.Context, actorID, roomID, state string) error {
	members, err := s.typingRoomMembers(ctx, roomID)
	if err != nil {
		return err
	}
	if state != "stop" {
		state = "start"
	}
	// 한 번의 순회로 (1) 발신자가 멤버인지 (2) 나머지 수신자가 누구인지를 함께 구한다.
	others := make([]string, 0, len(members))
	isMember := false
	for _, m := range members {
		if m == actorID { // 발신자 본인(다기기 포함)에겐 안 보냄
			isMember = true
			continue
		}
		others = append(others, m)
	}
	if !isMember {
		return domainmessaging.ErrNotMember
	}
	if len(others) == 0 {
		return nil
	}
	if payload, mErr := json.Marshal(outboundEvent{
		Type:   "typing",
		Typing: &typingDTO{RoomID: roomID, MemberID: actorID, State: state},
	}); mErr == nil {
		s.bc.Broadcast(others, payload)
	}
	return nil
}

// ReadStates 는 방의 멤버별 읽음 위치를 반환한다(멤버만 — 읽음 표시 렌더용).
func (s *Service) ReadStates(ctx context.Context, actorID, roomID string) ([]domainmessaging.ReadState, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	return s.rooms.ReadStates(ctx, roomID)
}

// RoomMembers 는 방의 멤버 ID 목록을 반환한다(멤버만).
func (s *Service) RoomMembers(ctx context.Context, actorID, roomID string) ([]string, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	return s.rooms.Members(ctx, roomID)
}

// RoomMembersDetail 은 방 멤버를 이름(username/display_name) 포함으로 반환한다(멤버십 스코프).
// 디렉터리에서 해석되지 않는 id 는 이름 없이(ID 만) 포함해 클라가 UUID 폴백할 수 있게 한다.
func (s *Service) RoomMembersDetail(ctx context.Context, actorID, roomID string) ([]domainmessaging.MemberInfo, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	ids, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return nil, err
	}
	names, err := s.directory.NamesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domainmessaging.MemberInfo, 0, len(ids))
	for _, id := range ids {
		if mi, ok := names[id]; ok {
			out = append(out, mi)
		} else {
			out = append(out, domainmessaging.MemberInfo{ID: id})
		}
	}
	return out, nil
}

// AddMembers 는 단체 방에 멤버를 추가한다(멤버만, group 한정). 추가 후 전체 멤버 목록을 반환하고
// 새로 추가된 멤버마다 member_joined 이벤트를 방 전원에게 브로드캐스트한다.
func (s *Service) AddMembers(ctx context.Context, actorID, roomID string, memberIDs []string) ([]string, error) {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return nil, err
	}
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if room.Type != domainmessaging.RoomGroup {
		return nil, domainmessaging.ErrInvalidRoom // 1:1 방은 멤버 변경 불가
	}
	existing, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return nil, err
	}
	have := make(map[string]struct{}, len(existing))
	for _, m := range existing {
		have[m] = struct{}{}
	}
	var added []string
	for _, m := range dedupe(memberIDs) {
		if _, ok := have[m]; !ok {
			added = append(added, m)
		}
	}
	if len(added) == 0 {
		return existing, nil // 변화 없음
	}
	if err := s.rooms.AddMembers(ctx, roomID, added, s.now().UTC().Unix()); err != nil {
		return nil, err
	}
	s.typingMembers.invalidate(roomID) // 신규 멤버가 즉시 타이핑을 받도록
	all := append(append([]string(nil), existing...), added...)
	for _, m := range added {
		s.broadcastMember(all, "member_joined", roomID, m)
	}
	return all, nil
}

// LeaveRoom 은 actor 가 단체 방에서 나간다(group 한정). 나가기 전 남은 멤버에게 member_left 를 브로드캐스트한다.
func (s *Service) LeaveRoom(ctx context.Context, actorID, roomID string) error {
	if err := s.requireMember(ctx, roomID, actorID); err != nil {
		return err
	}
	room, err := s.rooms.Get(ctx, roomID)
	if err != nil {
		return err
	}
	if room.Type != domainmessaging.RoomGroup {
		return domainmessaging.ErrInvalidRoom // 1:1 방은 나가기 불가
	}
	members, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return err
	}
	if err := s.rooms.RemoveMember(ctx, roomID, actorID); err != nil {
		return err
	}
	s.typingMembers.invalidate(roomID) // 나간 즉시 반영
	remaining := make([]string, 0, len(members))
	for _, m := range members {
		if m != actorID {
			remaining = append(remaining, m)
		}
	}
	s.broadcastMember(remaining, "member_left", roomID, actorID)
	return nil
}

// broadcastMember 는 멤버 변경(joined/left) 이벤트를 대상 멤버들에게 브로드캐스트한다.
func (s *Service) broadcastMember(targets []string, eventType, roomID, memberID string) {
	if len(targets) == 0 {
		return
	}
	if payload, err := json.Marshal(outboundEvent{
		Type:   eventType,
		Member: &memberEventDTO{RoomID: roomID, MemberID: memberID},
	}); err == nil {
		s.bc.Broadcast(targets, payload)
	}
}

// typingRoomMembers 는 타이핑 전파용 멤버 목록을 캐시 우선으로 반환한다.
// 반환 슬라이스는 캐시와 공유되므로 호출부가 변경해서는 안 된다(읽기 전용).
func (s *Service) typingRoomMembers(ctx context.Context, roomID string) ([]string, error) {
	if members, ok := s.typingMembers.get(roomID); ok {
		return members, nil
	}
	members, err := s.rooms.Members(ctx, roomID)
	if err != nil {
		return nil, err
	}
	s.typingMembers.put(roomID, members)
	return members, nil
}

func (s *Service) requireMember(ctx context.Context, roomID, memberID string) error {
	ok, err := s.rooms.IsMember(ctx, roomID, memberID)
	if err != nil {
		return err
	}
	if !ok {
		return domainmessaging.ErrNotMember
	}
	return nil
}

// outboundEvent 는 WS 로 내보내는 이벤트 포맷이다.
type outboundEvent struct {
	Type     string          `json:"type"` // message | message_edited | message_deleted | read | typing | member_joined | member_left | presence
	Message  *messageDTO     `json:"message,omitempty"`
	Read     *readDTO        `json:"read,omitempty"`
	Typing   *typingDTO      `json:"typing,omitempty"`
	Member   *memberEventDTO `json:"member,omitempty"`
	Presence *presenceDTO    `json:"presence,omitempty"`
}

// memberEventDTO 는 멤버 추가/나가기 이벤트다.
type memberEventDTO struct {
	RoomID   string `json:"room_id"`
	MemberID string `json:"member_id"`
}

// readDTO 는 read 이벤트(누가 어디까지 읽었는지)다.
type readDTO struct {
	RoomID     string `json:"room_id"`
	MemberID   string `json:"member_id"`
	LastReadAt int64  `json:"last_read_at"`
}

// typingDTO 는 typing 이벤트(누가 어느 방에서 타이핑 중인지)다.
type typingDTO struct {
	RoomID   string `json:"room_id"`
	MemberID string `json:"member_id"`
	State    string `json:"state"` // start | stop
}

type messageDTO struct {
	ID          string                       `json:"id"`
	RoomID      string                       `json:"room_id"`
	SenderID    string                       `json:"sender_id"`
	Content     string                       `json:"content"`
	CreatedAt   int64                        `json:"created_at"`
	EditedAt    int64                        `json:"edited_at,omitempty"`
	Deleted     bool                         `json:"deleted,omitempty"`
	Mentions    []string                     `json:"mentions,omitempty"`
	Attachments []domainmessaging.Attachment `json:"attachments,omitempty"`
}

func msgToDTO(m domainmessaging.Message) *messageDTO {
	return &messageDTO{
		ID: m.ID, RoomID: m.RoomID, SenderID: m.SenderID, Content: m.Content,
		CreatedAt: m.CreatedAt, EditedAt: m.EditedAt, Deleted: m.DeletedAt > 0,
		Mentions: m.Mentions, Attachments: m.Attachments,
	}
}

// presenceDTO 는 온라인 상태 변경 이벤트다.
type presenceDTO struct {
	MemberID string `json:"member_id"`
	Online   bool   `json:"online"`
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
