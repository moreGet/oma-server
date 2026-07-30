package db

import (
	"context"
	"database/sql"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainmessaging.MemberDirectory = (*MemberDirectoryRepository)(nil)
var _ domainagentregistry.MemberDirectory = (*MemberDirectoryRepository)(nil)

// MemberDirectoryRepository 는 멤버 ID → 표시 이름(username/display_name)을 members 테이블에서 해석한다.
// 채팅 멤버 이름 표시용 읽기 전용 어댑터(messaging.MemberDirectory 포트 구현).
type MemberDirectoryRepository struct {
	db *sql.DB
}

// NewMemberDirectoryRepository 는 MemberDirectoryRepository 를 생성한다.
func NewMemberDirectoryRepository(conn *sql.DB) *MemberDirectoryRepository {
	return &MemberDirectoryRepository{db: conn}
}

// NamesByIDs 는 주어진 멤버 ID 들의 이름 정보를 id→MemberInfo 맵으로 반환한다(없는 id 는 제외).
func (r *MemberDirectoryRepository) NamesByIDs(ctx context.Context, ids []string) (map[string]domainmessaging.MemberInfo, error) {
	out := make(map[string]domainmessaging.MemberInfo, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(ids)
	err := queryEach(ctx, r.db, "member directory", func(sc rowScanner) error {
		var id, username string
		var displayName sql.NullString
		if err := sc.Scan(&id, &username, &displayName); err != nil {
			return err
		}
		out[id] = domainmessaging.MemberInfo{ID: id, Username: username, DisplayName: displayName.String}
		return nil
	}, "SELECT id, username, display_name FROM members WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UsernamesByIDs 는 멤버 id → username 맵을 반환한다(agentregistry.MemberDirectory 포트 —
// 어드민 에이전트 목록의 owner 표시용. 없는 id 는 제외).
func (r *MemberDirectoryRepository) UsernamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	infos, err := r.NamesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(infos))
	for id, info := range infos {
		out[id] = info.Username
	}
	return out, nil
}
