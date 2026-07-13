package httpin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = (wsPongWait * 9) / 10
	wsMaxMessageSize = 64 * 1024
	wsOpTimeout      = 5 * time.Second // 인바운드 처리(DB) 작업당 데드라인
)

// chatWSUpgrader: 인증은 Bearer(SecureRouter)로 끝났으므로 Origin 검사는 허용(주 클라이언트는 C#).
var chatWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// ChatWSHandler 는 채팅 WebSocket(GET /api/v1/chat/ws) 핸들러다.
// 연결을 등록(presence 갱신)하고, 인바운드 send/typing 을 서비스로 중계한다.
type ChatWSHandler struct {
	svc messagingService
}

// NewChatWSHandler 는 ChatWSHandler 를 생성한다.
func NewChatWSHandler(svc messagingService) *ChatWSHandler {
	return &ChatWSHandler{svc: svc}
}

type wsInbound struct {
	Type        string                       `json:"type"` // send | typing
	RoomID      string                       `json:"room_id"`
	Content     string                       `json:"content"`
	State       string                       `json:"state"`       // typing: start | stop
	Mentions    []string                     `json:"mentions"`    // send: 멘션 멤버 ID
	Attachments []domainmessaging.Attachment `json:"attachments"` // send: 첨부 메타데이터
}

type wsErrorEvent struct {
	Type  string `json:"type"` // error
	Error string `json:"error"`
}

// Serve 는 WS 핸드셰이크 후 송수신 펌프를 구동한다(인증된 사용자).
func (h *ChatWSHandler) Serve(w http.ResponseWriter, r *http.Request) error {
	claims, _ := security.ClaimsFrom(r.Context())
	conn, err := chatWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil // Upgrade 가 자체적으로 HTTP 에러를 기록함
	}
	client := h.svc.Connect(r.Context(), claims.MemberID) // 허브 등록 + presence(online)

	go h.writePump(conn, client)
	h.readPump(conn, client, claims.MemberID)
	return nil
}

// readPump 는 클라이언트 인바운드를 읽어 메시지를 중계한다. 종료 시 연결 해제(presence offline).
func (h *ChatWSHandler) readPump(conn *websocket.Conn, client *messagingapp.Client, memberID string) {
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), wsOpTimeout)
		defer cancel()
		h.svc.Disconnect(ctx, client)
		_ = conn.Close()
	}()
	conn.SetReadLimit(wsMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var in wsInbound
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		switch in.Type {
		case "send":
			ctx, cancel := context.WithTimeout(context.Background(), wsOpTimeout)
			_, err := h.svc.SendMessage(ctx, memberID, in.RoomID, in.Content, in.Mentions, in.Attachments)
			cancel()
			if err != nil {
				// 발신자에게만 오류 통지(브로드캐스트는 안 함).
				if b, mErr := json.Marshal(wsErrorEvent{Type: "error", Error: messagingErr(err).Error()}); mErr == nil {
					select {
					case client.Send <- b:
					default:
					}
				}
			}
		case "typing":
			// 휘발성: 실패해도 무시(타이핑 신호는 best-effort).
			ctx, cancel := context.WithTimeout(context.Background(), wsOpTimeout)
			_ = h.svc.Typing(ctx, memberID, in.RoomID, in.State)
			cancel()
		}
	}
}

// writePump 는 허브가 client.Send 로 넣은 페이로드를 WS 로 내보내고 주기적 ping 으로 연결을 유지한다.
func (h *ChatWSHandler) writePump(conn *websocket.Conn, client *messagingapp.Client) {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()
	for {
		select {
		case msg, ok := <-client.Send:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok { // 허브가 채널을 닫음 → 종료
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
