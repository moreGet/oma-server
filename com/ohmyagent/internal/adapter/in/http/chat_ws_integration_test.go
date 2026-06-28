package httpin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	db "aiagent/com/ohmyagent/internal/adapter/out/db"
	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// TestChatWS_Integration 은 전체 WS 경로를 검증한다:
// gorilla 업그레이드 → 허브 등록 → u1 전송 → DB 영속 → 방 멤버 u2 가 실시간 수신.
func TestChatWS_Integration(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open("sqlite", "file:"+t.TempDir()+"/ws.db?_pragma=busy_timeout(5000)", 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, db.RunMigrations(ctx, "sqlite", conn))

	repo := db.NewMessagingRepository(conn, "sqlite")
	hub := messagingapp.NewHub()
	svc := messagingapp.NewService(repo, repo, db.NewChatAttachmentStore(conn), hub, nil)
	room, err := svc.CreateGroup(ctx, "u1", "team", []string{"u2"})
	require.NoError(t, err)

	h := NewChatWSHandler(svc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 테스트용 인증 주입: ?as=<memberID>
		c := security.WithClaims(r.Context(), domainauth.Claims{MemberID: r.URL.Query().Get("as")})
		_ = h.Serve(w, r.WithContext(c))
	}))
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	c2, _, err := websocket.DefaultDialer.Dial(wsBase+"?as=u2", nil) // 수신자
	require.NoError(t, err)
	defer func() { _ = c2.Close() }()
	c1, _, err := websocket.DefaultDialer.Dial(wsBase+"?as=u1", nil) // 발신자
	require.NoError(t, err)
	defer func() { _ = c1.Close() }()

	// 양쪽 연결이 허브에 등록될 때까지 대기(핸드셰이크와 Register 사이 레이스 방지).
	require.Eventually(t, func() bool { return hub.OnlineMembers() >= 2 }, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, c1.WriteJSON(map[string]string{"type": "send", "room_id": room.ID, "content": "hello ws"}))

	// presence 이벤트가 섞일 수 있으므로 message 가 올 때까지 읽는다.
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			RoomID   string `json:"room_id"`
			SenderID string `json:"sender_id"`
			Content  string `json:"content"`
		} `json:"message"`
	}
	for {
		_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := c2.ReadMessage()
		require.NoError(t, err, "u2 가 message 브로드캐스트를 받아야 함")
		require.NoError(t, json.Unmarshal(data, &ev))
		if ev.Type == "message" {
			break
		}
	}
	require.Equal(t, "hello ws", ev.Message.Content)
	require.Equal(t, "u1", ev.Message.SenderID)
	require.Equal(t, room.ID, ev.Message.RoomID)

	// DB 에도 저장됐는지 확인.
	msgs, err := repo.List(ctx, room.ID, 10, "")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "hello ws", msgs[0].Content)
}
