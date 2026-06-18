// Package logger 는 log/slog 기반 구조화 로거 초기화를 담당한다.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Options 는 로거 초기화 옵션이다.
type Options struct {
	Level  string // "debug" | "info" | "warn" | "error" (기본 info)
	Format string // "json" | "text" (기본 json)
}

// New 는 옵션에 따라 *slog.Logger 를 생성한다.
// 파싱 불가한 값은 안전한 기본값(info / json)으로 폴백한다.
func New(opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: parseLevel(opts.Level)}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, handlerOpts)
	default: // "json" 또는 미지정
		handler = slog.NewJSONHandler(os.Stdout, handlerOpts)
	}
	return slog.New(handler)
}

// Init 은 New 로 만든 로거를 slog 기본 로거로 등록하고 반환한다.
func Init(opts Options) *slog.Logger {
	l := New(opts)
	slog.SetDefault(l)
	return l
}

// parseLevel 은 문자열 레벨을 slog.Level 로 변환한다(기본 Info).
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
