package httpin

import (
	"bytes"
	"context"
	"crypto/sha256"
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

// 첨부 업로드 메모리 사용. ParseMultipartForm 의 인자는 "허용 최대 크기"가 아니라 "메모리 한도"라
// multipartMemoryBudget 보다 큰 파트는 임시 파일로 스필된다. 스필 경로에서도 바이트가
// 온전히 보존되는지가 이 변경의 회귀점이다(예전에는 전량이 RAM 에 있어 스필 경로를 안 탔다).

func newAttachmentServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open("sqlite", "file:"+t.TempDir()+"/spill.db?_pragma=busy_timeout(5000)", 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, db.RunMigrations(ctx, "sqlite", conn))

	repo := db.NewMessagingRepository(conn, "sqlite")
	svc := messagingapp.NewService(repo, repo, db.NewChatAttachmentStore(conn), messagingapp.NewHub(), nil)
	h := NewMessagingHandler(svc)

	withUser := func(next HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := security.WithClaims(r.Context(), domainauth.Claims{MemberID: "u1"})
			Handle(next)(w, r.WithContext(c))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat/attachments", withUser(h.UploadAttachment))
	mux.HandleFunc("GET /api/v1/chat/attachments/{aid}", withUser(h.DownloadAttachment))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// uploadBytes 는 payload 를 multipart 로 업로드하고 응답을 반환한다.
func uploadBytes(t *testing.T, srv *httptest.Server, name string, payload []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = fw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	resp, err := http.Post(srv.URL+"/api/v1/chat/attachments", mw.FormDataContentType(), &body)
	require.NoError(t, err)
	return resp
}

// 메모리 한도를 넘겨 디스크로 스필되는 크기의 파일이 바이트 단위로 온전히 왕복해야 한다.
func TestAttachmentUpload_SpilledToDiskRoundTripsIntact(t *testing.T) {
	srv := newAttachmentServer(t)

	// multipartMemoryBudget(1MiB)의 2배 — 반드시 임시 파일로 스필된다.
	payload := make([]byte, 2*multipartMemoryBudget)
	for i := range payload {
		payload[i] = byte(i % 251) // 위치 의존 패턴 — 잘림/어긋남을 잡아낸다
	}
	want := sha256.Sum256(payload)

	resp := uploadBytes(t, srv, "big.bin", payload)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var att domainmessaging.Attachment
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&att))
	_ = resp.Body.Close()
	assert.EqualValues(t, len(payload), att.SizeBytes)

	dl, err := http.Get(srv.URL + "/api/v1/chat/attachments/" + att.ID)
	require.NoError(t, err)
	defer func() { _ = dl.Body.Close() }()
	require.Equal(t, http.StatusOK, dl.StatusCode)

	got, err := io.ReadAll(dl.Body)
	require.NoError(t, err)
	require.Len(t, got, len(payload))
	assert.Equal(t, want, sha256.Sum256(got), "스필 경로에서 바이트가 변형되면 안 된다")
}

// 메모리 한도 이하(스필 없음) 경로도 그대로 동작해야 한다.
func TestAttachmentUpload_SmallFileStillWorks(t *testing.T) {
	srv := newAttachmentServer(t)

	payload := bytes.Repeat([]byte("a"), 1024)
	resp := uploadBytes(t, srv, "small.txt", payload)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var att domainmessaging.Attachment
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&att))
	_ = resp.Body.Close()
	assert.EqualValues(t, len(payload), att.SizeBytes)
}

// 상한 초과 파일은 전량을 읽기 전에 거부되어야 한다.
func TestAttachmentUpload_OversizeRejected(t *testing.T) {
	srv := newAttachmentServer(t)

	payload := make([]byte, domainmessaging.MaxAttachmentBytes+(64<<10)) // 10MiB + 64KiB
	resp := uploadBytes(t, srv, "toobig.bin", payload)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
