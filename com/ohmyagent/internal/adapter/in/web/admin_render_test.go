package web

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// renderPage 는 페이지 템플릿을 pageData 로 실행해 HTML 을 반환한다(렌더 오류 검출용).
func renderPage(t *testing.T, page string, pd pageData) string {
	t.Helper()
	tmpl := parsePages()[page]
	require.NotNil(t, tmpl, "템플릿 누락: %s", page)
	var sb strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&sb, "layout.html", pd))
	return sb.String()
}

func TestRenderChatAdmin(t *testing.T) {
	pd := pageData{
		Title: "채팅 관리", Active: "chat",
		User:      userView{Username: "admin", Role: "super_admin", Level: 2},
		CanManage: true, CanManageMembers: true,
		Data: chatView{
			Stats: chatStatsView{Rooms: 12, GroupRooms: 8, DirectRooms: 4, Messages: 3400, DeletedMessages: 12, Attachments: 5, AttachmentSize: "1.2 MB"},
			Rooms: []adminRoomRow{
				{ID: "r1", Type: "group", Name: "팀", MemberCount: 5, MessageCount: 120, LastActivity: "2026-06-28 10:00:00"},
				{ID: "r2", Type: "direct", Name: "1:1 대화", MemberCount: 2, MessageCount: 9, LastActivity: "2026-06-28 09:00:00"},
			},
		},
	}
	out := renderPage(t, "chat", pd)
	for _, want := range []string{"3,400", "1.2 MB", "/admin/chat/rooms/r1", "/admin/chat/rooms/r2/delete", "채팅 관리"} {
		require.Contains(t, out, want)
	}
}

func TestRenderChatRoomAdmin(t *testing.T) {
	pd := pageData{
		Title: "채팅 방", Active: "chat",
		User: userView{Username: "admin", Role: "super_admin", Level: 2}, CanManage: true,
		Data: chatRoomView{
			ID: "r1", Type: "group", Name: "팀", Members: []string{"u1", "u2"},
			Messages: []adminMsgRow{
				{ID: "m1", SenderID: "u1", Content: "hello", CreatedAt: "2026-06-28 10:00:00", Attachments: 1},
				{ID: "m2", SenderID: "u2", Content: "", CreatedAt: "2026-06-28 10:01:00", Deleted: true},
			},
		},
	}
	out := renderPage(t, "chat_room", pd)
	require.Contains(t, out, "/admin/chat/messages/m1/delete")    // 활성 메시지 삭제 폼
	require.Contains(t, out, "삭제된 메시지")                           // 삭제 메시지 표시
	require.NotContains(t, out, "/admin/chat/messages/m2/delete") // 이미 삭제된 건 삭제 폼 없음
}

func TestRenderClientAdmin(t *testing.T) {
	pd := pageData{
		Title: "클라이언트 버전", Active: "client",
		User: userView{Username: "admin", Role: "super_admin", Level: 2}, CanManage: true,
		Data: clientVersionView{Latest: "1.4.0", MinimumSupported: "1.2.0", Mandatory: true, UpdatedAt: "2026-06-28 10:00:00", UpdatedBy: "admin"},
	}
	out := renderPage(t, "client", pd)
	for _, want := range []string{`value="1.4.0"`, `value="1.2.0"`, "checked", "/admin/client"} {
		require.Contains(t, out, want)
	}
}
