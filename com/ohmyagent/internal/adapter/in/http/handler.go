// Package httpin 은 인바운드 HTTP 어댑터(핸들러·DTO·에러매핑)를 담는다.
// 모든 핸들러는 func(w,r) error 시그니처로 작성하고 Handle() 로 감싼다(스펙 §5.1).
package httpin

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// 에러 코드 상수(스펙 §5.2). AppError.Code 와 HTTPStatus() 매핑이 공유한다.
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeConflict         = "CONFLICT"
	CodeTooManyRequests  = "TOO_MANY_REQUESTS"
	CodeBadGateway       = "BAD_GATEWAY"
	CodeInternal         = "INTERNAL_ERROR"
	// 요청 본문 압축 수용에서 쓰는 코드(클라이언트 압축 스펙 §1).
	CodePayloadTooLarge      = "PAYLOAD_TOO_LARGE"      // 413 — 해제 후 크기 상한 초과(zip bomb 방어)
	CodeUnsupportedMediaType = "UNSUPPORTED_MEDIA_TYPE" // 415 — 지원하지 않는 Content-Encoding
)

// agent 계열 라우트(HandleAgent)의 중첩 envelope 에 쓰는 소문자 코드.
// 상태코드만으로는 구분되지 않는(400 이 bad_request 와 malformed_body 로 갈리는) 경우가 있어
// AppError 에 명시 코드를 실어 보낸다.
const (
	agentCodeUnsupportedEncoding = "unsupported_encoding"
	agentCodeMalformedBody       = "malformed_body"
	agentCodePayloadTooLarge     = "payload_too_large"
)

// 페이지네이션 기본값(스펙 §5.4): 기본 limit=20, 상한 100.
const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

// 요청 본문 크기 상한(메모리 보호 장치). 과대 본문은 디코딩 전 차단해 힙 폭증/GC 압력/OOM 을 막는다.
const (
	// maxJSONBytes: 일반 JSON 본문(인증/CRUD/설정 등).
	maxJSONBytes = 1 << 20 // 1 MiB
	// maxLargeJSONBytes: 첨부 base64 인라인·대화 본문 등 대용량 본문(agent/chat·세션·대화 push).
	maxLargeJSONBytes = 32 << 20 // 32 MiB
)

// decodeJSON 은 요청 본문을 maxBytes 로 제한해 JSON 디코딩한다.
// 상한 초과/형식 오류는 400(BAD_REQUEST)으로 매핑한다(MaxBytesReader 가 과대 본문을 조기 차단).
//
// Content-Encoding: gzip 이면 스트리밍으로 해제한다(compression.go). maxBytes 는 압축 전과
// 해제 후 양쪽에 걸리므로 zip bomb 으로 힙을 부풀릴 수 없다. 헤더가 없으면 종전과 동일하다.
func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	body, err := requestBodyReader(w, r, maxBytes)
	if err != nil {
		if ae := bodyReadErrToHTTP(err); ae != nil {
			return ae // 400 — gzip 헤더가 깨졌다
		}
		return err // 415 — 지원하지 않는 인코딩(이미 AppError)
	}
	defer func() { _ = body.Close() }()

	if err := json.NewDecoder(body).Decode(dst); err != nil {
		if ae := bodyReadErrToHTTP(err); ae != nil {
			return ae // 413(해제 상한 초과) / 400(깨진 압축 본문)
		}
		return ErrBadRequest("invalid request body")
	}
	return nil
}

// HandlerFunc 는 error 를 반환하는 핸들러 시그니처다.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle 은 HandlerFunc 를 http.HandlerFunc 로 변환한다.
// 패닉 복구(→500), 에러→AppError 변환, 5xx 로깅을 중앙에서 처리한다.
func Handle(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("handler panic", "error", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, &AppError{Code: CodeInternal, Message: "internal server error"})
			}
		}()
		if err := h(w, r); err != nil {
			ae := toAppError(err)
			if ae.HTTPStatus() >= 500 {
				slog.Error("request handler failed", "code", ae.Code, "error", err, "path", r.URL.Path)
			}
			writeJSON(w, ae.HTTPStatus(), ae)
		}
	}
}

// agentErrorBody 는 agent 대상 엔드포인트(health/models/agent/*)의 중첩 에러 envelope 이다.
// 클라이언트 계약: { "error": { "code": "...", "message": "..." } } (소문자 코드).
type agentErrorBody struct {
	Error agentErrorDetail `json:"error"`
}

type agentErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// agentCode 는 AppError 의 HTTP 상태를 클라이언트 계약의 소문자 코드로 매핑한다.
// 명시 코드(WithAgentCode)가 실려 있으면 그것을 우선한다 — 같은 400 이라도
// bad_request 와 malformed_body 는 클라이언트가 구분해야 하기 때문이다.
func agentCode(ae *AppError) string {
	if ae.agentCodeOverride != "" {
		return ae.agentCodeOverride
	}
	switch ae.HTTPStatus() {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "backend_error"
	}
}

// HandleAgent 는 Handle 과 같되, 에러를 클라이언트 계약의 중첩 envelope 으로 직렬화한다.
// (SSE 핸들러는 스트리밍 시작 후에는 nil 을 반환하고 에러를 SSE 이벤트로 보낸다.)
func HandleAgent(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("agent handler panic", "error", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, agentErrorBody{agentErrorDetail{Code: "backend_error", Message: "internal server error"}})
			}
		}()
		if err := h(w, r); err != nil {
			ae := toAppError(err)
			if ae.HTTPStatus() >= 500 {
				slog.Error("agent request failed", "code", ae.Code, "error", err, "path", r.URL.Path)
			}
			writeJSON(w, ae.HTTPStatus(), agentErrorBody{agentErrorDetail{Code: agentCode(ae), Message: ae.Message}})
		}
	}
}

// AppError 는 전 API 공통 에러 envelope 이다(스펙 §5.2).
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// cause 는 원인 에러다. json 태그가 없으므로 응답 본문에는 절대 직렬화되지 않고,
	// Error() 를 통해 서버 로그에만 남는다. 업스트림 실패(502 등)를 조사하려면
	// 클라이언트에 노출할 수 없는 상세(벤더 응답·엔드포인트)가 로그에 필요하다.
	cause error
	// agentCodeOverride 는 HandleAgent 의 중첩 envelope 에서 쓸 소문자 코드다.
	// 비어 있으면 agentCode() 가 HTTP 상태에서 유도한다. json 태그가 없어 flat envelope
	// 응답에는 영향을 주지 않는다(관리자 API 는 종전대로 대문자 Code 만 나간다).
	agentCodeOverride string
}

// WithCause 는 원인 에러를 매단 사본을 반환한다(로그 전용, 클라이언트 응답에는 노출되지 않는다).
func (e *AppError) WithCause(err error) *AppError {
	if e == nil || err == nil {
		return e
	}
	return &AppError{Code: e.Code, Message: e.Message, cause: err, agentCodeOverride: e.agentCodeOverride}
}

// WithAgentCode 는 agent 계열 envelope 에서 쓸 소문자 코드를 매단 사본을 반환한다.
func (e *AppError) WithAgentCode(code string) *AppError {
	if e == nil || code == "" {
		return e
	}
	return &AppError{Code: e.Code, Message: e.Message, cause: e.cause, agentCodeOverride: code}
}

func (e *AppError) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

// HTTPStatus 는 code 에 대응하는 HTTP 상태코드를 반환한다.
func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeConflict:
		return http.StatusConflict
	case CodeTooManyRequests:
		return http.StatusTooManyRequests
	case CodePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case CodeBadGateway:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// --- 생성 헬퍼 ---

func ErrBadRequest(msg string) *AppError   { return &AppError{Code: CodeBadRequest, Message: msg} }
func ErrUnauthorized(msg string) *AppError { return &AppError{Code: CodeUnauthorized, Message: msg} }
func ErrForbidden(msg string) *AppError    { return &AppError{Code: CodeForbidden, Message: msg} }
func ErrNotFound(msg string) *AppError     { return &AppError{Code: CodeNotFound, Message: msg} }
func ErrConflict(msg string) *AppError     { return &AppError{Code: CodeConflict, Message: msg} }
func ErrTooManyRequests(msg string) *AppError {
	return &AppError{Code: CodeTooManyRequests, Message: msg}
}
func ErrBadGateway(msg string) *AppError { return &AppError{Code: CodeBadGateway, Message: msg} }
func ErrPayloadTooLarge(msg string) *AppError {
	return &AppError{Code: CodePayloadTooLarge, Message: msg}
}
func ErrUnsupportedMediaType(msg string) *AppError {
	return &AppError{Code: CodeUnsupportedMediaType, Message: msg}
}

// toAppError 는 임의 에러를 AppError 로 변환한다. AppError 가 아니면 500 으로 폴백한다.
func toAppError(err error) *AppError {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return &AppError{Code: CodeInternal, Message: "internal server error"}
}

// clampLimit 은 페이지네이션 limit 을 [1, maxPageLimit] 범위로 보정한다.
// 0 이하이면 기본값(defaultPageLimit), 상한 초과면 maxPageLimit.
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultPageLimit
	}
	if limit > maxPageLimit {
		return maxPageLimit
	}
	return limit
}

// writeJSON 은 상태코드와 body 를 JSON 으로 직렬화해 응답한다(스펙 §5.5).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// sseBufPool 은 SSE 프레임 직렬화용 버퍼를 재사용한다(토큰당 할당 제거 — 고동접 스트리밍 GC 압력 완화).
var sseBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// writeSSE 는 payload 를 `data: {json}\n\n` 형식으로 1회 Write 한다(스펙: POST /chat).
// fmt.Fprintf(리플렉션 포맷팅 + 포맷 문자열 할당) 대신 풀링한 버퍼에 직접 프레이밍한다.
func writeSSE(w http.ResponseWriter, payload any) error {
	buf := sseBufPool.Get().(*bytes.Buffer)
	defer func() { buf.Reset(); sseBufPool.Put(buf) }()
	buf.WriteString("data: ")
	if err := json.NewEncoder(buf).Encode(payload); err != nil { // Encode 가 끝에 \n 1개 추가
		return err
	}
	buf.WriteByte('\n') // 빈 줄로 이벤트 종료 → 총 `data: {json}\n\n`
	_, err := w.Write(buf.Bytes())
	return err
}

// writeSSEEvent 는 `event: <name>\ndata: {json}\n\n` 형식으로 1회 Write 한다(스펙: POST /agent/chat).
func writeSSEEvent(w http.ResponseWriter, event string, payload any) error {
	buf := sseBufPool.Get().(*bytes.Buffer)
	defer func() { buf.Reset(); sseBufPool.Put(buf) }()
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	if err := json.NewEncoder(buf).Encode(payload); err != nil { // Encode 가 끝에 \n 1개 추가
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// writeSSEHeaders 는 SSE 응답 헤더(+200)를 기록하고 write deadline 을 해제한다(장시간 스트리밍 보존).
// chat/agent 스트리밍 핸들러가 첫 조각 직전에 1회 호출한다(지연 기록).
func writeSSEHeaders(w http.ResponseWriter, rc *http.ResponseController) {
	_ = rc.SetWriteDeadline(time.Time{}) // 스트리밍: write deadline 해제(서버 WriteTimeout 우회)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 버퍼링 비활성
	w.WriteHeader(http.StatusOK)
}

// atoiDefault 는 문자열을 정수로 파싱하고, 실패 시 def 를 반환한다.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
