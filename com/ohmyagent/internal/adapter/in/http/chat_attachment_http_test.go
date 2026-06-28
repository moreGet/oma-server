package httpin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiagent/com/ohmyagent/internal/adapter/in/http/security"
	db "aiagent/com/ohmyagent/internal/adapter/out/db"
	messagingapp "aiagent/com/ohmyagent/internal/application/messaging"
	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// TestAttachmentUploadDownload_HTTP 는 multipart 업로드 → JSON 메타데이터 → 바이너리 다운로드 왕복을 검증한다.
func TestAttachmentUploadDownload_HTTP(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open("sqlite", "file:"+t.TempDir()+"/att.db?_pragma=busy_timeout(5000)", 1)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, db.RunMigrations(ctx, "sqlite", conn))

	repo := db.NewMessagingRepository(conn, "sqlite")
	svc := messagingapp.NewService(repo, repo, db.NewChatAttachmentStore(conn), messagingapp.NewHub(), nil)
	h := NewMessagingHandler(svc)

	withUser := func(next HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := security.WithClaims(r.Context(), domainauth.Claims{MemberID: "u1"})
			Handle(next)(w, r.WithContext(c)) // 에러는 Handle 이 처리
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat/attachments", withUser(h.UploadAttachment))
	mux.HandleFunc("GET /api/v1/chat/attachments/{aid}", withUser(h.DownloadAttachment))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// multipart 파일 업로드.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "hello.txt")
	require.NoError(t, err)
	_, _ = fw.Write([]byte("hello file body"))
	require.NoError(t, mw.Close())

	resp, err := http.Post(srv.URL+"/api/v1/chat/attachments", mw.FormDataContentType(), &body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var att domainmessaging.Attachment
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&att))
	_ = resp.Body.Close()
	require.NotEmpty(t, att.ID)
	assert.Equal(t, "hello.txt", att.FileName)
	assert.Equal(t, int64(len("hello file body")), att.SizeBytes)
	require.Equal(t, "/api/v1/chat/attachments/"+att.ID, att.URL)

	// 다운로드(바이너리 + 헤더).
	dl, err := http.Get(srv.URL + att.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, dl.StatusCode)
	got, _ := io.ReadAll(dl.Body)
	_ = dl.Body.Close()
	assert.Equal(t, "hello file body", string(got))
	assert.Contains(t, dl.Header.Get("Content-Disposition"), "hello.txt")

	// 없는 첨부 → 404.
	miss, err := http.Get(srv.URL + "/api/v1/chat/attachments/nope")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, miss.StatusCode)
	_ = miss.Body.Close()
}
