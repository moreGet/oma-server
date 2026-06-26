// Package httpin 은 인바운드 HTTP 어댑터(핸들러·DTO·에러매핑)를 담는다.
// 모든 핸들러는 func(w,r) error 시그니처로 작성하고 Handle() 로 감싼다(스펙 §5.1).
package httpin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
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
)

// 페이지네이션 기본값(스펙 §5.4): 기본 limit=20, 상한 100.
const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

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
func agentCode(ae *AppError) string {
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
}

func (e *AppError) Error() string { return e.Code + ": " + e.Message }

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
