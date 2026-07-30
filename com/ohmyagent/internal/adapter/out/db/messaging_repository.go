package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// 컴파일 타임 인터페이스 만족 검증.
var (
	_ domainmessaging.RoomRepository    = (*MessagingRepository)(nil)
	_ domainmessaging.MessageRepository = (*MessagingRepository)(nil)
)

// MessagingRepository 는 채팅 방/멤버십/메시지를 RDB 에 영속화한다.
type MessagingRepository struct {
	db     *sql.DB
	driver string // mysql | sqlite (멤버 추가 idempotent upsert 분기)
}

// NewMessagingRepository 는 MessagingRepository 를 생성한다.
func NewMessagingRepository(conn *sql.DB, driver string) *MessagingRepository {
	return &MessagingRepository{db: conn, driver: driver}
}

// Create 는 방과 멤버를 한 트랜잭션으로 생성한다.
func (r *MessagingRepository) Create(ctx context.Context, room domainmessaging.Room, memberIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("messaging: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO chat_rooms (id, type, name, direct_key, created_by, created_at) VALUES (?,?,?,?,?,?)",
		room.ID, string(room.Type), nullString(room.Name), nullString(room.DirectKey), nullString(room.CreatedBy), room.CreatedAt,
	); err != nil {
		return fmt.Errorf("messaging: insert room: %w", err)
	}
	if len(memberIDs) > 0 {
		// 멤버 1건씩 N회 INSERT 대신 멀티로우 1쿼리(sqlite 단일 writer 에서 왕복 절감).
		rows := make([]string, 0, len(memberIDs))
		args := make([]any, 0, len(memberIDs)*3)
		for _, mid := range memberIDs {
			rows = append(rows, "(?,?,?)")
			args = append(args, room.ID, mid, room.CreatedAt)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO chat_room_members (room_id, member_id, joined_at) VALUES "+strings.Join(rows, ","),
			args...,
		); err != nil {
			return fmt.Errorf("messaging: insert members: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("messaging: commit: %w", err)
	}
	return nil
}

const roomCols = "id, type, name, direct_key, created_by, created_at"

// scanID 는 단일 문자열 컬럼(member_id 등) 결과를 읽는다.
func scanID(s rowScanner) (string, error) {
	var id string
	err := s.Scan(&id)
	return id, err
}

func scanReadState(s rowScanner) (domainmessaging.ReadState, error) {
	var rs domainmessaging.ReadState
	err := s.Scan(&rs.MemberID, &rs.LastReadAt)
	return rs, err
}

func scanAdminRoom(s rowScanner) (domainmessaging.AdminRoom, error) {
	var (
		ar                 domainmessaging.AdminRoom
		typ                string
		name, dkey, crtdBy sql.NullString
	)
	if err := s.Scan(&ar.ID, &typ, &name, &dkey, &crtdBy, &ar.CreatedAt, &ar.MemberCount, &ar.MessageCount, &ar.LastActivity); err != nil {
		return domainmessaging.AdminRoom{}, err
	}
	ar.Type = domainmessaging.RoomType(typ)
	ar.Name, ar.DirectKey, ar.CreatedBy = name.String, dkey.String, crtdBy.String
	return ar, nil
}

func scanRoom(s rowScanner) (domainmessaging.Room, error) {
	var (
		room               domainmessaging.Room
		typ                string
		name, dkey, crtdBy sql.NullString
	)
	if err := s.Scan(&room.ID, &typ, &name, &dkey, &crtdBy, &room.CreatedAt); err != nil {
		return domainmessaging.Room{}, err
	}
	room.Type = domainmessaging.RoomType(typ)
	room.Name, room.DirectKey, room.CreatedBy = name.String, dkey.String, crtdBy.String
	return room, nil
}

// Get 은 방을 반환한다(없으면 ErrRoomNotFound).
func (r *MessagingRepository) Get(ctx context.Context, roomID string) (domainmessaging.Room, error) {
	return queryOne(ctx, r.db, "messaging: get room", domainmessaging.ErrRoomNotFound, scanRoom,
		"SELECT "+roomCols+" FROM chat_rooms WHERE id=?", roomID)
}

// FindDirect 는 정준 키로 기존 1:1 방을 찾는다(없으면 ErrRoomNotFound).
func (r *MessagingRepository) FindDirect(ctx context.Context, directKey string) (domainmessaging.Room, error) {
	return queryOne(ctx, r.db, "messaging: find direct", domainmessaging.ErrRoomNotFound, scanRoom,
		"SELECT "+roomCols+" FROM chat_rooms WHERE direct_key=?", directKey)
}

// ListForMember 는 멤버가 속한 방을 최근 활동(마지막 메시지)순으로 반환한다.
func (r *MessagingRepository) ListForMember(ctx context.Context, memberID string) ([]domainmessaging.Room, error) {
	return queryList(ctx, r.db, "messaging: list rooms", scanRoom,
		"SELECT r.id, r.type, r.name, r.direct_key, r.created_by, r.created_at "+
			"FROM chat_rooms r JOIN chat_room_members m ON m.room_id = r.id "+
			"WHERE m.member_id = ? "+
			"ORDER BY COALESCE((SELECT MAX(created_at) FROM chat_messages cm WHERE cm.room_id = r.id), r.created_at) DESC",
		memberID)
}

// Members 는 방의 멤버 ID 목록을 반환한다.
func (r *MessagingRepository) Members(ctx context.Context, roomID string) ([]string, error) {
	return queryList(ctx, r.db, "messaging: members", scanID,
		"SELECT member_id FROM chat_room_members WHERE room_id=?", roomID)
}

// IsMember 는 멤버가 방에 속하는지 반환한다.
func (r *MessagingRepository) IsMember(ctx context.Context, roomID, memberID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, "SELECT 1 FROM chat_room_members WHERE room_id=? AND member_id=?", roomID, memberID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("messaging: is member: %w", err)
	}
	return true, nil
}

// Stats 는 방/메시지 집계를 반환한다(어드민 대시보드).
func (r *MessagingRepository) Stats(ctx context.Context) (domainmessaging.AdminStats, error) {
	var s domainmessaging.AdminStats
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(CASE WHEN type='group' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN type='direct' THEN 1 ELSE 0 END),0) FROM chat_rooms").
		Scan(&s.Rooms, &s.GroupRooms, &s.DirectRooms); err != nil {
		return domainmessaging.AdminStats{}, fmt.Errorf("messaging: room stats: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(CASE WHEN deleted_at=0 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN deleted_at>0 THEN 1 ELSE 0 END),0) FROM chat_messages").
		Scan(&s.Messages, &s.DeletedMessages); err != nil {
		return domainmessaging.AdminStats{}, fmt.Errorf("messaging: message stats: %w", err)
	}
	return s, nil
}

// ListAllRooms 는 전체 방을 최근 활동순(집계 포함)으로 반환한다(어드민).
func (r *MessagingRepository) ListAllRooms(ctx context.Context, limit int) ([]domainmessaging.AdminRoom, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return queryList(ctx, r.db, "messaging: list all rooms", scanAdminRoom,
		"SELECT r.id, r.type, r.name, r.direct_key, r.created_by, r.created_at, "+
			"(SELECT COUNT(*) FROM chat_room_members m WHERE m.room_id=r.id), "+
			"(SELECT COUNT(*) FROM chat_messages cm WHERE cm.room_id=r.id), "+
			"COALESCE((SELECT MAX(created_at) FROM chat_messages cm WHERE cm.room_id=r.id), r.created_at) AS last_act "+
			"FROM chat_rooms r ORDER BY last_act DESC LIMIT ?", limit)
}

// DeleteRoom 은 방 + 멤버십 + 메시지를 한 트랜잭션으로 삭제한다(어드민). 첨부 바이너리는 유지.
func (r *MessagingRepository) DeleteRoom(ctx context.Context, roomID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("messaging: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		"DELETE FROM chat_messages WHERE room_id=?",
		"DELETE FROM chat_room_members WHERE room_id=?",
		"DELETE FROM chat_rooms WHERE id=?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, roomID); err != nil {
			return fmt.Errorf("messaging: delete room: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("messaging: commit: %w", err)
	}
	return nil
}

// MarkRead 는 멤버의 읽음 위치를 전진시킨다(현재보다 클 때만 — 단조 증가).
func (r *MessagingRepository) MarkRead(ctx context.Context, roomID, memberID string, readAt int64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE chat_room_members SET last_read_at=? WHERE room_id=? AND member_id=? AND last_read_at < ?",
		readAt, roomID, memberID, readAt,
	); err != nil {
		return fmt.Errorf("messaging: mark read: %w", err)
	}
	return nil
}

// UnreadByRoom 은 멤버의 방별 안읽음 수(last_read_at 이후 남이 보낸 메시지)를 한 쿼리로 반환한다.
func (r *MessagingRepository) UnreadByRoom(ctx context.Context, memberID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT m.room_id, COUNT(cm.id) "+
			"FROM chat_room_members m "+
			"LEFT JOIN chat_messages cm ON cm.room_id = m.room_id AND cm.created_at > m.last_read_at AND cm.sender_id <> m.member_id AND cm.deleted_at = 0 "+
			"WHERE m.member_id = ? GROUP BY m.room_id",
		memberID)
	if err != nil {
		return nil, fmt.Errorf("messaging: unread by room: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var roomID string
		var n int
		if err := rows.Scan(&roomID, &n); err != nil {
			return nil, fmt.Errorf("messaging: scan unread: %w", err)
		}
		out[roomID] = n
	}
	return out, rows.Err()
}

// ReadStates 는 방의 멤버별 읽음 위치를 반환한다.
func (r *MessagingRepository) ReadStates(ctx context.Context, roomID string) ([]domainmessaging.ReadState, error) {
	return queryList(ctx, r.db, "messaging: read states", scanReadState,
		"SELECT member_id, last_read_at FROM chat_room_members WHERE room_id=?", roomID)
}

// AddMembers 는 멤버를 방에 추가한다(이미 멤버면 무시). last_read_at 은 joinedAt 으로 시작(가입 전 메시지 안읽음 제외).
func (r *MessagingRepository) AddMembers(ctx context.Context, roomID string, memberIDs []string, joinedAt int64) error {
	if len(memberIDs) == 0 {
		return nil
	}
	tail := " ON CONFLICT(room_id, member_id) DO NOTHING" // sqlite
	verb := "INSERT INTO"
	if r.driver == "mysql" {
		verb, tail = "INSERT IGNORE INTO", ""
	}
	// 멤버 1건씩 N회 INSERT 대신 멀티로우 1쿼리(왕복 절감).
	rows := make([]string, 0, len(memberIDs))
	args := make([]any, 0, len(memberIDs)*4)
	for _, mid := range memberIDs {
		rows = append(rows, "(?,?,?,?)")
		args = append(args, roomID, mid, joinedAt, joinedAt)
	}
	q := verb + " chat_room_members (room_id, member_id, joined_at, last_read_at) VALUES " + strings.Join(rows, ",") + tail
	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("messaging: add members: %w", err)
	}
	return nil
}

// RemoveMember 는 멤버를 방에서 제거한다(나가기).
func (r *MessagingRepository) RemoveMember(ctx context.Context, roomID, memberID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM chat_room_members WHERE room_id=? AND member_id=?", roomID, memberID); err != nil {
		return fmt.Errorf("messaging: remove member: %w", err)
	}
	return nil
}

// Save 는 메시지를 저장한다(멘션/첨부 메타데이터 포함).
func (r *MessagingRepository) Save(ctx context.Context, m domainmessaging.Message) error {
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO chat_messages (id, room_id, sender_id, content, created_at, mentions, attachments) VALUES (?,?,?,?,?,?,?)",
		m.ID, m.RoomID, m.SenderID, m.Content, m.CreatedAt, encodeJSONList(m.Mentions), encodeJSONList(m.Attachments),
	); err != nil {
		return fmt.Errorf("messaging: save message: %w", err)
	}
	return nil
}

// CoMembers 는 member 와 한 방이라도 공유하는 모든 멤버 ID(본인 포함, 중복 제거)를 반환한다.
func (r *MessagingRepository) CoMembers(ctx context.Context, memberID string) ([]string, error) {
	return queryList(ctx, r.db, "messaging: co-members", scanID,
		"SELECT DISTINCT m2.member_id FROM chat_room_members m1 JOIN chat_room_members m2 ON m1.room_id = m2.room_id WHERE m1.member_id = ?",
		memberID)
}

// Mentioning 은 memberID 가 멘션된 최신 메시지(삭제 제외)를 limit 개 반환한다.
// JSON LIKE 매칭은 ID 가 JSON 토큰("<id>")으로 둘러싸여 부분문자열 오탐을 피한다.
func (r *MessagingRepository) Mentioning(ctx context.Context, memberID string, limit int) ([]domainmessaging.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return queryList(ctx, r.db, "messaging: mentioning", scanMessage,
		"SELECT "+messageCols+" FROM chat_messages WHERE deleted_at=0 AND mentions LIKE ? ORDER BY created_at DESC LIMIT ?",
		"%\""+memberID+"\"%", limit)
}

const messageCols = "id, room_id, sender_id, content, created_at, edited_at, deleted_at, mentions, attachments"

func scanMessage(s rowScanner) (domainmessaging.Message, error) {
	var (
		m                     domainmessaging.Message
		mentions, attachments sql.NullString
	)
	if err := s.Scan(&m.ID, &m.RoomID, &m.SenderID, &m.Content, &m.CreatedAt, &m.EditedAt, &m.DeletedAt, &mentions, &attachments); err != nil {
		return domainmessaging.Message{}, err
	}
	m.Mentions = decodeJSONList[string](mentions.String)
	m.Attachments = decodeJSONList[domainmessaging.Attachment](attachments.String)
	return m, nil
}

// List 는 방의 메시지를 최신순으로 반환한다(beforeID 가 있으면 그 메시지 이전 페이지). 삭제 메시지도 포함(content 빔).
func (r *MessagingRepository) List(ctx context.Context, roomID string, limit int, beforeID string) ([]domainmessaging.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := "SELECT " + messageCols + " FROM chat_messages WHERE room_id=?"
	args := []any{roomID}
	if beforeID != "" {
		query += " AND created_at < (SELECT created_at FROM chat_messages WHERE id=?)"
		args = append(args, beforeID)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	return queryList(ctx, r.db, "messaging: list messages", scanMessage, query, args...)
}

// Get 은 메시지 한 건을 반환한다(없으면 ErrMessageNotFound).
func (r *MessagingRepository) GetMessage(ctx context.Context, messageID string) (domainmessaging.Message, error) {
	return queryOne(ctx, r.db, "messaging: get message", domainmessaging.ErrMessageNotFound, scanMessage,
		"SELECT "+messageCols+" FROM chat_messages WHERE id=?", messageID)
}

// UpdateContent 는 메시지 본문을 수정하고 editedAt 을 기록한다(삭제된 건 제외).
func (r *MessagingRepository) UpdateContent(ctx context.Context, messageID, content string, editedAt int64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE chat_messages SET content=?, edited_at=? WHERE id=? AND deleted_at=0",
		content, editedAt, messageID,
	); err != nil {
		return fmt.Errorf("messaging: update content: %w", err)
	}
	return nil
}

// MarkDeleted 는 메시지를 소프트 삭제한다(deletedAt 기록 + content 비움).
func (r *MessagingRepository) MarkDeleted(ctx context.Context, messageID string, deletedAt int64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE chat_messages SET deleted_at=?, content='' WHERE id=?",
		deletedAt, messageID,
	); err != nil {
		return fmt.Errorf("messaging: mark deleted: %w", err)
	}
	return nil
}
