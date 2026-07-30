package httpin

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoJSON 은 decodeJSON 으로 본문을 읽어 그대로 되돌려주는 테스트 핸들러다.
// 압축 해제가 디코더 안에서 일어나므로 이 핸들러는 압축을 전혀 알지 못한다(그게 요점이다).
func echoJSON(maxBytes int64) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var body map[string]any
		if err := decodeJSON(w, r, maxBytes, &body); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, body)
		return nil
	}
}

// newCompressionMux 는 같은 핸들러를 두 envelope(flat / 중첩)로 등록한 mux 를 만든다.
func newCompressionMux(maxBytes int64) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /flat", Handle(echoJSON(maxBytes)))
	mux.HandleFunc("POST /agent", HandleAgent(echoJSON(maxBytes)))
	return mux
}

func gzipBytes(t *testing.T, plain string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(plain))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func postBody(mux *http.ServeMux, path string, body []byte, encoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// agentErrCode 는 중첩 envelope 의 error.code 를 꺼낸다.
func agentErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Error.Code
}

// flatErrCode 는 flat envelope 의 code 를 꺼낸다.
func flatErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var ae AppError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ae))
	return ae.Code
}

func TestRequestCompression_Gzip(t *testing.T) {
	const payload = `{"model":"claude","stream":false,"note":"안녕하세요 hello"}`
	mux := newCompressionMux(maxJSONBytes)

	t.Run("헤더가 없으면 종전대로 평문 처리(하위 호환)", func(t *testing.T) {
		w := postBody(mux, "/agent", []byte(payload), "")

		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, payload, w.Body.String())
	})

	t.Run("identity 는 평문으로 처리한다", func(t *testing.T) {
		w := postBody(mux, "/agent", []byte(payload), "identity")

		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, payload, w.Body.String())
	})

	t.Run("gzip 본문은 평문과 완전히 같은 결과를 낸다", func(t *testing.T) {
		plain := postBody(mux, "/agent", []byte(payload), "")
		gz := postBody(mux, "/agent", gzipBytes(t, payload), "gzip")

		require.Equal(t, http.StatusOK, gz.Code)
		assert.JSONEq(t, plain.Body.String(), gz.Body.String(),
			"수용 기준: 평문 요청과 gzip 요청의 결과가 동일해야 한다")
	})

	t.Run("대소문자·공백이 섞인 헤더도 gzip 으로 인식한다", func(t *testing.T) {
		w := postBody(mux, "/agent", gzipBytes(t, payload), "  GZip ")

		require.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, payload, w.Body.String())
	})
}

func TestRequestCompression_UnsupportedEncoding(t *testing.T) {
	mux := newCompressionMux(maxJSONBytes)
	const payload = `{"a":1}`

	// deflate/br 은 스펙상 선택이라 구현하지 않는다 — 조용히 깨지지 않고 415 로 거절해야 한다.
	for _, enc := range []string{"deflate", "br", "compress", "gzip, gzip"} {
		t.Run("agent 계열: "+enc+" 는 415 unsupported_encoding", func(t *testing.T) {
			w := postBody(mux, "/agent", []byte(payload), enc)

			assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
			assert.Equal(t, "unsupported_encoding", agentErrCode(t, w))
		})
	}

	t.Run("관리자 API 는 기존 flat envelope 을 유지한다", func(t *testing.T) {
		w := postBody(mux, "/flat", []byte(payload), "br")

		assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
		assert.Equal(t, CodeUnsupportedMediaType, flatErrCode(t, w))
	})
}

func TestRequestCompression_MalformedBody(t *testing.T) {
	mux := newCompressionMux(maxJSONBytes)

	t.Run("gzip 이라고 했는데 평문이면 400 malformed_body", func(t *testing.T) {
		w := postBody(mux, "/agent", []byte(`{"a":1}`), "gzip")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "malformed_body", agentErrCode(t, w))
	})

	t.Run("본문이 비어도 400 malformed_body", func(t *testing.T) {
		w := postBody(mux, "/agent", nil, "gzip")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "malformed_body", agentErrCode(t, w))
	})

	t.Run("스트림이 중간에 잘리면 400 malformed_body", func(t *testing.T) {
		full := gzipBytes(t, `{"v":"`+strings.Repeat("x", 4096)+`"}`)
		truncated := full[:len(full)/2] // gzip 헤더는 살아 있고 본문만 절단

		w := postBody(mux, "/agent", truncated, "gzip")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "malformed_body", agentErrCode(t, w))
	})

	t.Run("관리자 API 는 flat BAD_REQUEST", func(t *testing.T) {
		w := postBody(mux, "/flat", []byte(`{"a":1}`), "gzip")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, CodeBadRequest, flatErrCode(t, w))
	})

	t.Run("압축과 무관한 JSON 문법 오류는 종전대로 BAD_REQUEST", func(t *testing.T) {
		w := postBody(mux, "/flat", []byte(`{"a":`), "")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, CodeBadRequest, flatErrCode(t, w))
	})
}

func TestRequestCompression_ZipBombDefense(t *testing.T) {
	const limit = 1024
	mux := newCompressionMux(limit)

	t.Run("해제 후 상한을 넘으면 413 — 압축 바이트는 상한보다 훨씬 작아도 막는다", func(t *testing.T) {
		// 반복 문자는 gzip 이 극단적으로 잘 줄인다: 해제 후 64KiB, 압축 후 수십 바이트.
		// 상한 없이 전량 해제하면 작은 요청 하나로 힙을 부풀릴 수 있다는 것이 요점이다.
		bomb := gzipBytes(t, `{"v":"`+strings.Repeat("a", 64<<10)+`"}`)
		require.Less(t, len(bomb), limit, "압축 바이트 자체는 상한 미만이어야 시험이 성립한다")

		w := postBody(mux, "/agent", bomb, "gzip")

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Equal(t, "payload_too_large", agentErrCode(t, w))
	})

	t.Run("해제 크기가 정확히 상한이면 통과한다(경계)", func(t *testing.T) {
		// `{"v":"` (6) + n + `"}` (2) == limit
		payload := `{"v":"` + strings.Repeat("a", limit-8) + `"}`
		require.Len(t, payload, limit)

		w := postBody(mux, "/agent", gzipBytes(t, payload), "gzip")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("상한보다 1바이트 크면 413(경계)", func(t *testing.T) {
		payload := `{"v":"` + strings.Repeat("a", limit-7) + `"}`
		require.Len(t, payload, limit+1)

		w := postBody(mux, "/agent", gzipBytes(t, payload), "gzip")

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Equal(t, "payload_too_large", agentErrCode(t, w))
	})

	t.Run("관리자 API 는 flat PAYLOAD_TOO_LARGE", func(t *testing.T) {
		bomb := gzipBytes(t, `{"v":"`+strings.Repeat("a", 64<<10)+`"}`)

		w := postBody(mux, "/flat", bomb, "gzip")

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Equal(t, CodePayloadTooLarge, flatErrCode(t, w))
	})

	t.Run("평문 과대 본문은 종전 동작(400)을 유지한다", func(t *testing.T) {
		// 압축 경로를 넣으면서 기존 평문 상한 처리가 바뀌지 않았는지 확인한다.
		w := postBody(mux, "/flat", []byte(`{"v":"`+strings.Repeat("a", 64<<10)+`"}`), "")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, CodeBadRequest, flatErrCode(t, w))
	})
}

func TestLimitedDecompressor_OverLimitIsSticky(t *testing.T) {
	// 상한을 넘긴 뒤에도 Read 가 (0, nil) 을 돌려주면 호출자(json.Decoder)가 진행 없이
	// 영원히 되돌아온다. 초과 이후에는 몇 번을 불러도 errPayloadTooLarge 로 끊겨야 한다.
	zr, err := gzip.NewReader(bytes.NewReader(gzipBytes(t, strings.Repeat("a", 4096))))
	require.NoError(t, err)
	d := &limitedDecompressor{zr: zr, limit: 16}

	buf := make([]byte, 64)
	var lastErr error
	for i := 0; i < 100 && lastErr == nil; i++ {
		_, lastErr = d.Read(buf)
	}
	require.ErrorIs(t, lastErr, errPayloadTooLarge, "상한을 넘겼는데도 끊기지 않았다")

	for i := 0; i < 3; i++ {
		n, err := d.Read(buf)
		assert.Zero(t, n)
		assert.ErrorIs(t, err, errPayloadTooLarge, "초과 이후 재호출이 성공을 반환하면 무한 루프가 된다")
	}
}

func TestRequestCompression_RatioIsBoundedByAbsoluteLimit(t *testing.T) {
	// 압축비가 아무리 커도 해제 누적 바이트가 상한에서 끊기므로 메모리는 상한에 묶인다.
	// (스펙이 제안한 "압축비 100:1" 대신 절대 상한을 쓰는 근거를 고정한다.)
	const limit = 4096
	mux := newCompressionMux(limit)

	bomb := gzipBytes(t, `{"v":"`+strings.Repeat("a", 8<<20)+`"}`) // 해제 시 8MiB
	require.Less(t, len(bomb), 64<<10)

	w := postBody(mux, "/agent", bomb, "gzip")

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Equal(t, "payload_too_large", agentErrCode(t, w),
		fmt.Sprintf("압축 %dB → 해제 8MiB 인데도 상한 %dB 에서 끊겨야 한다", len(bomb), limit))
}
