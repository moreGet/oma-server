package apperror

import "errors"

// 핸들러 / 미들웨어가 공용으로 사용할 sentinel 에러 모음.
// 도메인 에러는 핸들러에서 errors.Is 로 분기한 후, 필요 시 이 sentinel 들로 매핑된다.
var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrBadRequest   = errors.New("bad request")
)
