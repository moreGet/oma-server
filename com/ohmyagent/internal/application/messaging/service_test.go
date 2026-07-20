package messagingapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// --- 인메모리 fake 레포 ---

type fakeRepo struct {
	rooms    map[string]domainmessaging.Room
	members  map[string][]string         // roomID -> memberIDs
	directs  map[string]string           // directKey -> roomID
	lastRead map[string]map[string]int64 // roomID -> memberID -> lastReadAt
	messages []domainmessaging.Message
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rooms: map[string]domainmessaging.Room{}, members: map[string][]string{}, directs: map[string]string{}, lastRead: map[string]map[string]int64{}}
}

// fakeAttStore 는 AttachmentStore 테스트 더블이다(RoomRepository.Get 과 충돌하므로 별도 타입).
type fakeAttStore struct {
	atts map[string]struct {
		meta domainmessaging.StoredAttachment
		data []byte
	}
}

func newAttStore() *fakeAttStore {
	return &fakeAttStore{atts: map[string]struct {
		meta domainmessaging.StoredAttachment
		data []byte
	}{}}
}
func (s *fakeAttStore) Put(_ context.Context, a domainmessaging.StoredAttachment, data []byte) error {
	s.atts[a.ID] = struct {
		meta domainmessaging.StoredAttachment
		data []byte
	}{a, data}
	return nil
}
func (s *fakeAttStore) Open(_ context.Context, id string) (domainmessaging.StoredAttachment, io.ReadCloser, error) {
	if v, ok := s.atts[id]; ok {
		return v.meta, io.NopCloser(bytes.NewReader(v.data)), nil
	}
	return domainmessaging.StoredAttachment{}, nil, domainmessaging.ErrAttachmentNotFound
}
func (s *fakeAttStore) Stats(_ context.Context) (int, int64, error) {
	var bytes int64
	for _, v := range s.atts {
		bytes += v.meta.SizeBytes
	}
	return len(s.atts), bytes, nil
}

func (r *fakeRepo) Create(_ context.Context, room domainmessaging.Room, memberIDs []string) error {
	r.rooms[room.ID] = room
	r.members[room.ID] = append([]string(nil), memberIDs...)
	if room.DirectKey != "" {
		r.directs[room.DirectKey] = room.ID
	}
	return nil
}
func (r *fakeRepo) Get(_ context.Context, roomID string) (domainmessaging.Room, error) {
	if rm, ok := r.rooms[roomID]; ok {
		return rm, nil
	}
	return domainmessaging.Room{}, domainmessaging.ErrRoomNotFound
}
func (r *fakeRepo) FindDirect(_ context.Context, key string) (domainmessaging.Room, error) {
	if id, ok := r.directs[key]; ok {
		return r.rooms[id], nil
	}
	return domainmessaging.Room{}, domainmessaging.ErrRoomNotFound
}
func (r *fakeRepo) ListForMember(_ context.Context, memberID string) ([]domainmessaging.Room, error) {
	var out []domainmessaging.Room
	for id, mem := range r.members {
		for _, m := range mem {
			if m == memberID {
				out = append(out, r.rooms[id])
				break
			}
		}
	}
	return out, nil
}
func (r *fakeRepo) Members(_ context.Context, roomID string) ([]string, error) {
	return r.members[roomID], nil
}
func (r *fakeRepo) IsMember(_ context.Context, roomID, memberID string) (bool, error) {
	for _, m := range r.members[roomID] {
		if m == memberID {
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeRepo) MarkRead(_ context.Context, roomID, memberID string, readAt int64) error {
	if r.lastRead[roomID] == nil {
		r.lastRead[roomID] = map[string]int64{}
	}
	if readAt > r.lastRead[roomID][memberID] {
		r.lastRead[roomID][memberID] = readAt
	}
	return nil
}
func (r *fakeRepo) UnreadByRoom(_ context.Context, memberID string) (map[string]int, error) {
	out := map[string]int{}
	for roomID, mem := range r.members {
		isMember := false
		for _, m := range mem {
			if m == memberID {
				isMember = true
				break
			}
		}
		if !isMember {
			continue
		}
		last := r.lastRead[roomID][memberID]
		n := 0
		for _, msg := range r.messages {
			if msg.RoomID == roomID && msg.CreatedAt > last && msg.SenderID != memberID {
				n++
			}
		}
		out[roomID] = n
	}
	return out, nil
}
func (r *fakeRepo) ReadStates(_ context.Context, roomID string) ([]domainmessaging.ReadState, error) {
	var out []domainmessaging.ReadState
	for _, m := range r.members[roomID] {
		out = append(out, domainmessaging.ReadState{MemberID: m, LastReadAt: r.lastRead[roomID][m]})
	}
	return out, nil
}
func (r *fakeRepo) AddMembers(_ context.Context, roomID string, memberIDs []string, _ int64) error {
	for _, mid := range memberIDs {
		dup := false
		for _, m := range r.members[roomID] {
			if m == mid {
				dup = true
				break
			}
		}
		if !dup {
			r.members[roomID] = append(r.members[roomID], mid)
		}
	}
	return nil
}
func (r *fakeRepo) CoMembers(_ context.Context, memberID string) ([]string, error) {
	set := map[string]struct{}{}
	for roomID, mem := range r.members {
		inRoom := false
		for _, m := range mem {
			if m == memberID {
				inRoom = true
				break
			}
		}
		if inRoom {
			for _, m := range r.members[roomID] {
				set[m] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	return out, nil
}
func (r *fakeRepo) Mentioning(_ context.Context, memberID string, _ int) ([]domainmessaging.Message, error) {
	var out []domainmessaging.Message
	for _, m := range r.messages {
		if m.DeletedAt > 0 {
			continue
		}
		for _, mn := range m.Mentions {
			if mn == memberID {
				out = append(out, m)
				break
			}
		}
	}
	return out, nil
}
func (r *fakeRepo) Stats(_ context.Context) (domainmessaging.AdminStats, error) {
	st := domainmessaging.AdminStats{Rooms: len(r.rooms)}
	for _, rm := range r.rooms {
		if rm.Type == domainmessaging.RoomDirect {
			st.DirectRooms++
		} else {
			st.GroupRooms++
		}
	}
	for _, m := range r.messages {
		if m.DeletedAt > 0 {
			st.DeletedMessages++
		} else {
			st.Messages++
		}
	}
	return st, nil
}
func (r *fakeRepo) ListAllRooms(_ context.Context, _ int) ([]domainmessaging.AdminRoom, error) {
	var out []domainmessaging.AdminRoom
	for id, rm := range r.rooms {
		ar := domainmessaging.AdminRoom{Room: rm, MemberCount: len(r.members[id]), LastActivity: rm.CreatedAt}
		for _, m := range r.messages {
			if m.RoomID == id {
				ar.MessageCount++
			}
		}
		out = append(out, ar)
	}
	return out, nil
}
func (r *fakeRepo) DeleteRoom(_ context.Context, roomID string) error {
	delete(r.rooms, roomID)
	delete(r.members, roomID)
	out := r.messages[:0]
	for _, m := range r.messages {
		if m.RoomID != roomID {
			out = append(out, m)
		}
	}
	r.messages = out
	return nil
}
func (r *fakeRepo) RemoveMember(_ context.Context, roomID, memberID string) error {
	cur := r.members[roomID]
	out := cur[:0:0]
	for _, m := range cur {
		if m != memberID {
			out = append(out, m)
		}
	}
	r.members[roomID] = out
	return nil
}
func (r *fakeRepo) Save(_ context.Context, m domainmessaging.Message) error {
	r.messages = append(r.messages, m)
	return nil
}
func (r *fakeRepo) List(_ context.Context, roomID string, _ int, _ string) ([]domainmessaging.Message, error) {
	var out []domainmessaging.Message
	for _, m := range r.messages {
		if m.RoomID == roomID {
			out = append(out, m)
		}
	}
	return out, nil
}
func (r *fakeRepo) GetMessage(_ context.Context, id string) (domainmessaging.Message, error) {
	for _, m := range r.messages {
		if m.ID == id {
			return m, nil
		}
	}
	return domainmessaging.Message{}, domainmessaging.ErrMessageNotFound
}
func (r *fakeRepo) UpdateContent(_ context.Context, id, content string, editedAt int64) error {
	for i := range r.messages {
		if r.messages[i].ID == id {
			r.messages[i].Content, r.messages[i].EditedAt = content, editedAt
		}
	}
	return nil
}
func (r *fakeRepo) MarkDeleted(_ context.Context, id string, deletedAt int64) error {
	for i := range r.messages {
		if r.messages[i].ID == id {
			r.messages[i].DeletedAt, r.messages[i].Content = deletedAt, ""
		}
	}
	return nil
}

func TestService_DirectDedupe(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, repo, newAttStore(), NewHub(), nil)
	ctx := context.Background()

	a, err := s.CreateDirect(ctx, "u1", "u2")
	require.NoError(t, err)
	b, err := s.CreateDirect(ctx, "u2", "u1") // 순서 바꿔도 같은 방
	require.NoError(t, err)
	assert.Equal(t, a.ID, b.ID, "1:1 방은 정준키로 중복 생성 안 됨")

	_, err = s.CreateDirect(ctx, "u1", "u1") // 자기 자신 불가
	assert.ErrorIs(t, err, domainmessaging.ErrInvalidRoom)
}

func TestService_SendEnforcesMembershipAndBroadcasts(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()

	room, err := s.CreateGroup(ctx, "u1", "team", []string{"u2"})
	require.NoError(t, err)

	// 비멤버는 전송 불가.
	_, err = s.SendMessage(ctx, "stranger", room.ID, "hi", nil, nil)
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)

	// 멤버 u2 의 연결을 허브에 등록 → 브로드캐스트 수신 확인.
	client := &Client{MemberID: "u2", Send: make(chan []byte, 8)}
	hub.Register(client)

	msg, err := s.SendMessage(ctx, "u1", room.ID, "  hello  ", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", msg.Content) // trim
	assert.Len(t, repo.messages, 1)       // 영속화됨

	select {
	case payload := <-client.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		assert.Equal(t, "message", ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "hello", ev.Message.Content)
		assert.Equal(t, "u1", ev.Message.SenderID)
	default:
		t.Fatal("멤버 u2 에게 브로드캐스트가 전달되지 않음")
	}
}

func TestService_UnreadAndMarkRead(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})

	_, _ = s.SendMessage(ctx, "u1", room.ID, "a", nil, nil)
	_, _ = s.SendMessage(ctx, "u1", room.ID, "b", nil, nil)

	// u2 는 u1 이 보낸 2건이 안읽음, u1 은 본인 메시지라 0.
	u2, _ := s.UnreadByRoom(ctx, "u2")
	assert.Equal(t, 2, u2[room.ID])
	u1, _ := s.UnreadByRoom(ctx, "u1")
	assert.Equal(t, 0, u1[room.ID])

	// u1 의 연결 등록 → u2 의 읽음 이벤트 수신 확인.
	c1 := &Client{MemberID: "u1", Send: make(chan []byte, 8)}
	hub.Register(c1)

	readAt, err := s.MarkRead(ctx, "u2", room.ID)
	require.NoError(t, err)
	assert.Positive(t, readAt)

	// 읽음 후 u2 안읽음 0.
	u2after, _ := s.UnreadByRoom(ctx, "u2")
	assert.Equal(t, 0, u2after[room.ID])

	// read 이벤트 브로드캐스트.
	select {
	case payload := <-c1.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		assert.Equal(t, "read", ev.Type)
		require.NotNil(t, ev.Read)
		assert.Equal(t, "u2", ev.Read.MemberID)
		assert.Equal(t, room.ID, ev.Read.RoomID)
	default:
		t.Fatal("read 이벤트가 브로드캐스트되지 않음")
	}

	// 읽음 표시 상태에 u2 의 위치가 반영.
	states, err := s.ReadStates(ctx, "u1", room.ID)
	require.NoError(t, err)
	var u2read int64
	for _, st := range states {
		if st.MemberID == "u2" {
			u2read = st.LastReadAt
		}
	}
	assert.Equal(t, readAt, u2read)
}

func TestService_Typing(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})

	c1 := &Client{MemberID: "u1", Send: make(chan []byte, 8)} // 발신자
	hub.Register(c1)
	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 8)} // 상대
	hub.Register(c2)

	// 비멤버 불가.
	assert.ErrorIs(t, s.Typing(ctx, "stranger", room.ID, "start"), domainmessaging.ErrNotMember)

	// u1 타이핑 → u2 수신, u1(본인) 미수신.
	require.NoError(t, s.Typing(ctx, "u1", room.ID, "garbage")) // 이상값 → start 로 정규화
	select {
	case payload := <-c2.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		assert.Equal(t, "typing", ev.Type)
		require.NotNil(t, ev.Typing)
		assert.Equal(t, "u1", ev.Typing.MemberID)
		assert.Equal(t, "start", ev.Typing.State)
		assert.Equal(t, room.ID, ev.Typing.RoomID)
	default:
		t.Fatal("u2 가 typing 을 받지 못함")
	}
	select {
	case <-c1.Send:
		t.Fatal("발신자 본인에게 typing 이 전달되면 안 됨")
	default:
	}

	// stop 도 정상 전달.
	require.NoError(t, s.Typing(ctx, "u1", room.ID, "stop"))
	payload := <-c2.Send
	var ev outboundEvent
	require.NoError(t, json.Unmarshal(payload, &ev))
	assert.Equal(t, "stop", ev.Typing.State)
}

func TestService_AddAndLeaveMembers(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})

	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 8)}
	hub.Register(c2)

	// 비멤버는 추가 불가.
	_, err := s.AddMembers(ctx, "stranger", room.ID, []string{"u3"})
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)

	// u1 이 u3 추가(이미 멤버 u2 는 무시).
	members, err := s.AddMembers(ctx, "u1", room.ID, []string{"u3", "u2"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2", "u3"}, members)

	// u2 는 member_joined(u3) 수신.
	select {
	case payload := <-c2.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		assert.Equal(t, "member_joined", ev.Type)
		require.NotNil(t, ev.Member)
		assert.Equal(t, "u3", ev.Member.MemberID)
	default:
		t.Fatal("member_joined 가 전달되지 않음")
	}

	// 1:1 방은 멤버 변경 불가.
	direct, _ := s.CreateDirect(ctx, "u1", "u9")
	_, err = s.AddMembers(ctx, "u1", direct.ID, []string{"u3"})
	assert.ErrorIs(t, err, domainmessaging.ErrInvalidRoom)
	assert.ErrorIs(t, s.LeaveRoom(ctx, "u1", direct.ID), domainmessaging.ErrInvalidRoom)

	// u3 가 나가기 → 남은 멤버 u1 이 member_left 수신.
	c1 := &Client{MemberID: "u1", Send: make(chan []byte, 8)}
	hub.Register(c1)
	require.NoError(t, s.LeaveRoom(ctx, "u3", room.ID))
	remaining, _ := s.RoomMembers(ctx, "u1", room.ID)
	assert.ElementsMatch(t, []string{"u1", "u2"}, remaining)
	select {
	case payload := <-c1.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		assert.Equal(t, "member_left", ev.Type)
		assert.Equal(t, "u3", ev.Member.MemberID)
	default:
		t.Fatal("member_left 가 전달되지 않음")
	}

	// 나간 u3 는 더 이상 전송 불가.
	_, err = s.SendMessage(ctx, "u3", room.ID, "still here?", nil, nil)
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)
}

func TestService_EditAndDeleteMessage(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})
	msg, _ := s.SendMessage(ctx, "u1", room.ID, "original", nil, nil)

	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 8)}
	hub.Register(c2)

	// 남의 메시지는 수정/삭제 불가.
	_, err := s.EditMessage(ctx, "u2", room.ID, msg.ID, "hacked")
	assert.ErrorIs(t, err, domainmessaging.ErrNotSender)
	assert.ErrorIs(t, s.DeleteMessage(ctx, "u2", room.ID, msg.ID), domainmessaging.ErrNotSender)

	// 없는 메시지 → 404.
	_, err = s.EditMessage(ctx, "u1", room.ID, "nope", "x")
	assert.ErrorIs(t, err, domainmessaging.ErrMessageNotFound)

	// 본인 수정 → message_edited 브로드캐스트 + edited_at 기록.
	edited, err := s.EditMessage(ctx, "u1", room.ID, msg.ID, "  edited  ")
	require.NoError(t, err)
	assert.Equal(t, "edited", edited.Content)
	assert.Positive(t, edited.EditedAt)
	assertEvent(t, c2, "message_edited", func(ev outboundEvent) {
		assert.Equal(t, "edited", ev.Message.Content)
		assert.Positive(t, ev.Message.EditedAt)
	})

	// 본인 삭제 → message_deleted 브로드캐스트 + content 비움.
	require.NoError(t, s.DeleteMessage(ctx, "u1", room.ID, msg.ID))
	assertEvent(t, c2, "message_deleted", func(ev outboundEvent) {
		assert.True(t, ev.Message.Deleted)
		assert.Empty(t, ev.Message.Content)
	})

	// 이력에는 삭제 메시지가 (순서 유지 위해) 남되 deleted 표시.
	hist, _ := s.History(ctx, "u1", room.ID, 50, "")
	require.Len(t, hist, 1)
	assert.Positive(t, hist[0].DeletedAt)
	assert.Empty(t, hist[0].Content)

	// 삭제된 메시지 재삭제는 idempotent(에러 없음), 수정은 404.
	require.NoError(t, s.DeleteMessage(ctx, "u1", room.ID, msg.ID))
	_, err = s.EditMessage(ctx, "u1", room.ID, msg.ID, "again")
	assert.ErrorIs(t, err, domainmessaging.ErrMessageNotFound) // 삭제된 건 수정 불가? → ownMessage 통과 후 UpdateContent(WHERE deleted_at=0) 무효
}

// assertEvent 는 클라이언트가 다음으로 받는 이벤트를 타입 검증하고 추가 검사를 실행한다.
func assertEvent(t *testing.T, c *Client, wantType string, check func(outboundEvent)) {
	t.Helper()
	select {
	case payload := <-c.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		require.Equal(t, wantType, ev.Type)
		require.NotNil(t, ev.Message)
		check(ev)
	default:
		t.Fatalf("%s 이벤트가 전달되지 않음", wantType)
	}
}

func recv(t *testing.T, c *Client) outboundEvent {
	t.Helper()
	select {
	case p := <-c.Send:
		var ev outboundEvent
		require.NoError(t, json.Unmarshal(p, &ev))
		return ev
	default:
		t.Fatal("이벤트 없음")
		return outboundEvent{}
	}
}

func TestService_Presence(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})

	c2 := s.Connect(ctx, "u2") // u2 online (본인 presence 한 건 비움)
	_ = recv(t, c2)
	c1 := s.Connect(ctx, "u1") // u1 online → co-member u2 에게 전파

	ev := recv(t, c2)
	assert.Equal(t, "presence", ev.Type)
	require.NotNil(t, ev.Presence)
	assert.Equal(t, "u1", ev.Presence.MemberID)
	assert.True(t, ev.Presence.Online)

	online, _ := s.RoomPresence(ctx, "u1", room.ID)
	assert.ElementsMatch(t, []string{"u1", "u2"}, online)

	s.Disconnect(ctx, c1)
	ev = recv(t, c2)
	assert.Equal(t, "presence", ev.Type)
	assert.False(t, ev.Presence.Online)

	online2, _ := s.RoomPresence(ctx, "u2", room.ID)
	assert.ElementsMatch(t, []string{"u2"}, online2)
}

func TestService_Kick(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2", "u3"}) // u1 = 생성자

	// 비생성자는 강퇴 불가.
	assert.ErrorIs(t, s.KickMember(ctx, "u2", room.ID, "u3"), domainmessaging.ErrNotRoomOwner)
	// 본인 강퇴(=leave 사용) 불가, 비멤버 강퇴 불가.
	assert.ErrorIs(t, s.KickMember(ctx, "u1", room.ID, "u1"), domainmessaging.ErrInvalidRoom)
	assert.ErrorIs(t, s.KickMember(ctx, "u1", room.ID, "ghost"), domainmessaging.ErrNotMember)

	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 8)}
	hub.Register(c2)
	require.NoError(t, s.KickMember(ctx, "u1", room.ID, "u3"))
	mem, _ := s.RoomMembers(ctx, "u1", room.ID)
	assert.ElementsMatch(t, []string{"u1", "u2"}, mem)
	ev := recv(t, c2)
	assert.Equal(t, "member_left", ev.Type)
	assert.Equal(t, "u3", ev.Member.MemberID)
}

// fakeDirectory 는 멤버 이름 해석 테스트 더블이다.
type fakeDirectory struct {
	names map[string]domainmessaging.MemberInfo
}

func (d fakeDirectory) NamesByIDs(_ context.Context, ids []string) (map[string]domainmessaging.MemberInfo, error) {
	out := make(map[string]domainmessaging.MemberInfo, len(ids))
	for _, id := range ids {
		if mi, ok := d.names[id]; ok {
			out[id] = mi
		}
	}
	return out, nil
}

func TestService_RoomMembersDetail(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, repo, newAttStore(), NewHub(), nil)
	s.SetMemberDirectory(fakeDirectory{names: map[string]domainmessaging.MemberInfo{
		"u1": {ID: "u1", Username: "admin", DisplayName: "신성현"},
		"u2": {ID: "u2", Username: "probe2"}, // display_name 없음 → 클라가 username 폴백
	}})
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2", "u3"}) // u3 은 디렉터리에 없음

	// 비멤버는 403(멤버십 스코프).
	_, err := s.RoomMembersDetail(ctx, "ghost", room.ID)
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)

	infos, err := s.RoomMembersDetail(ctx, "u1", room.ID)
	require.NoError(t, err)
	byID := make(map[string]domainmessaging.MemberInfo, len(infos))
	for _, mi := range infos {
		byID[mi.ID] = mi
	}
	assert.Equal(t, "admin", byID["u1"].Username)
	assert.Equal(t, "신성현", byID["u1"].DisplayName)
	assert.Equal(t, "probe2", byID["u2"].Username)
	assert.Empty(t, byID["u2"].DisplayName)
	// 디렉터리 미해석 멤버는 ID 만(클라 UUID 폴백).
	assert.Equal(t, domainmessaging.MemberInfo{ID: "u3"}, byID["u3"])
}

func TestService_MentionsAndAttachments(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, repo, newAttStore(), NewHub(), nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})

	att := []domainmessaging.Attachment{{FileName: "a.png", ContentType: "image/png", SizeBytes: 123, URL: "http://x/a.png"}}
	// 멘션에 비멤버(stranger) 포함 → 검증으로 제거. 첨부 포함.
	msg, err := s.SendMessage(ctx, "u1", room.ID, "hey", []string{"u2", "stranger"}, att)
	require.NoError(t, err)
	assert.Equal(t, []string{"u2"}, msg.Mentions)
	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, "a.png", msg.Attachments[0].FileName)

	// 멘션 피드: u2 는 1건, u1 은 0건.
	feed, _ := s.MentionsFeed(ctx, "u2", 50)
	require.Len(t, feed, 1)
	assert.Equal(t, "hey", feed[0].Content)
	feed1, _ := s.MentionsFeed(ctx, "u1", 50)
	assert.Empty(t, feed1)

	// 첨부만 있고 본문 없는 메시지도 허용.
	_, err = s.SendMessage(ctx, "u1", room.ID, "", nil, att)
	require.NoError(t, err)
	// 본문도 첨부도 없으면 거부.
	_, err = s.SendMessage(ctx, "u1", room.ID, "   ", nil, nil)
	assert.ErrorIs(t, err, domainmessaging.ErrInvalidRoom)
}

func TestService_Attachments(t *testing.T) {
	repo := newFakeRepo()
	att := newAttStore()
	s := NewService(repo, repo, att, NewHub(), nil)
	ctx := context.Background()

	a, err := s.UploadAttachment(ctx, "u1", "photo.png", "image/png", []byte("binarydata"))
	require.NoError(t, err)
	assert.NotEmpty(t, a.ID)
	assert.Equal(t, "photo.png", a.FileName)
	assert.Equal(t, int64(10), a.SizeBytes)
	assert.Equal(t, "/api/v1/chat/attachments/"+a.ID, a.URL)

	sa, rc, err := s.DownloadAttachment(ctx, a.ID)
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "photo.png", sa.FileName)
	assert.Equal(t, "image/png", sa.ContentType)
	assert.Equal(t, "u1", sa.UploaderID)
	assert.Equal(t, []byte("binarydata"), data)

	// 빈 파일 / 초과 크기 / 없는 id.
	_, err = s.UploadAttachment(ctx, "u1", "x", "", nil)
	assert.ErrorIs(t, err, domainmessaging.ErrAttachmentEmpty)
	big := make([]byte, domainmessaging.MaxAttachmentBytes+1)
	_, err = s.UploadAttachment(ctx, "u1", "big", "", big)
	assert.ErrorIs(t, err, domainmessaging.ErrAttachmentTooLarge)
	_, _, err = s.DownloadAttachment(ctx, "nope")
	assert.ErrorIs(t, err, domainmessaging.ErrAttachmentNotFound)
}

func TestService_Admin(t *testing.T) {
	repo := newFakeRepo()
	hub := NewHub()
	s := NewService(repo, repo, newAttStore(), hub, nil)
	ctx := context.Background()
	g, _ := s.CreateGroup(ctx, "u1", "team", []string{"u2"})
	_, _ = s.CreateDirect(ctx, "u1", "u3")
	m1, _ := s.SendMessage(ctx, "u1", g.ID, "hello", nil, nil)
	_, _ = s.SendMessage(ctx, "u2", g.ID, "world", nil, nil)

	// 집계.
	st, err := s.AdminStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, st.Rooms)
	assert.Equal(t, 1, st.GroupRooms)
	assert.Equal(t, 1, st.DirectRooms)
	assert.Equal(t, 2, st.Messages)

	// 전체 방 목록.
	rooms, _ := s.AdminListRooms(ctx, 50)
	assert.Len(t, rooms, 2)

	// 모더레이션 삭제 → message_deleted 브로드캐스트(멤버 u2 수신).
	c2 := &Client{MemberID: "u2", Send: make(chan []byte, 8)}
	hub.Register(c2)
	require.NoError(t, s.AdminDeleteMessage(ctx, m1.ID))
	ev := recv(t, c2)
	assert.Equal(t, "message_deleted", ev.Type)
	st2, _ := s.AdminStats(ctx)
	assert.Equal(t, 1, st2.Messages)
	assert.Equal(t, 1, st2.DeletedMessages)

	// 방 삭제.
	require.NoError(t, s.AdminDeleteRoom(ctx, g.ID))
	st3, _ := s.AdminStats(ctx)
	assert.Equal(t, 1, st3.Rooms) // direct 만 남음
}

func TestService_HistoryRequiresMembership(t *testing.T) {
	repo := newFakeRepo()
	s := NewService(repo, repo, newAttStore(), NewHub(), nil)
	ctx := context.Background()
	room, _ := s.CreateGroup(ctx, "u1", "team", nil)

	_, err := s.History(ctx, "stranger", room.ID, 50, "")
	assert.ErrorIs(t, err, domainmessaging.ErrNotMember)

	_, err = s.History(ctx, "u1", room.ID, 50, "")
	assert.NoError(t, err)
}
