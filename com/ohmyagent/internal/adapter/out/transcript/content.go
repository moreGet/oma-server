package transcriptout

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"time"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// marshalGzip 은 전체 대화 이력(메타데이터 포함)을 JSON 으로 직렬화 후 gzip 압축한다.
// file/s3 백엔드는 메타데이터 컬럼이 없으므로 본문에 모두 포함한다.
func marshalGzip(t domaintranscript.Transcript) ([]byte, error) {
	rec := map[string]any{
		"id":                t.ID,
		"member_id":         t.MemberID,
		"session_id":        t.SessionID,
		"source":            string(t.Source),
		"model":             t.Model,
		"request":           json.RawMessage(t.Request),
		"response":          t.Response,
		"prompt_tokens":     t.PromptTokens,
		"completion_tokens": t.CompletionTokens,
		"total_tokens":      t.TotalTokens,
		"finish_reason":     t.FinishReason,
		"created_at":        t.CreatedAt.UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("transcript: marshal: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, fmt.Errorf("transcript: gzip write: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("transcript: gzip close: %w", err)
	}
	return buf.Bytes(), nil
}
