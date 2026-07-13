package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	domaintranscript "aiagent/com/ohmyagent/internal/domain/transcript"
)

// gzipWriterPool 은 gzip.Writer 를 재사용한다(이력 저장이 요청마다 발생 → 매번 할당 시 GC 압력).
var gzipWriterPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// gzipBytes 는 payload 를 풀에서 빌린 gzip.Writer 로 압축해 새 []byte 로 반환한다.
func gzipBytes(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzipWriterPool.Get().(*gzip.Writer)
	defer gzipWriterPool.Put(zw)
	zw.Reset(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 컴파일 타임 인터페이스 만족 검증.
var (
	_ domaintranscript.Store  = (*TranscriptStore)(nil)
	_ domaintranscript.Purger = (*TranscriptStore)(nil)
)

// TranscriptStore 는 대화 이력을 chat_transcripts 테이블에 저장한다(domaintranscript.Store 구현).
// 본문(요청+응답)은 JSON 직렬화 후 gzip 압축해 content BLOB 에, 조회/보존용 메타데이터는 별도 컬럼에 둔다.
type TranscriptStore struct {
	db *sql.DB
}

// NewTranscriptStore 는 TranscriptStore 를 생성한다.
func NewTranscriptStore(conn *sql.DB) *TranscriptStore {
	return &TranscriptStore{db: conn}
}

// transcriptContent 는 gzip 압축 대상(본문) 구조다.
type transcriptContent struct {
	Request  json.RawMessage `json:"request,omitempty"`
	Response string          `json:"response"`
}

// Save 는 한 건의 대화 이력을 gzip 압축 본문과 함께 INSERT 한다.
func (s *TranscriptStore) Save(ctx context.Context, t domaintranscript.Transcript) error {
	payload, err := json.Marshal(transcriptContent{Request: json.RawMessage(t.Request), Response: t.Response})
	if err != nil {
		return fmt.Errorf("transcript: marshal content: %w", err)
	}
	content, err := gzipBytes(payload)
	if err != nil {
		return fmt.Errorf("transcript: gzip: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO chat_transcripts
		   (id, member_id, session_id, source, model, prompt_tokens, completion_tokens, total_tokens, finish_reason, content, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, nullString(t.MemberID), nullString(t.SessionID), string(t.Source), t.Model,
		t.PromptTokens, t.CompletionTokens, t.TotalTokens, t.FinishReason, content, t.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("transcript: save id=%s: %w", t.ID, err)
	}
	return nil
}

// Purge 는 olderThan 이전(created_at <)의 이력을 삭제하고 삭제 건수를 반환한다(보존 정책).
func (s *TranscriptStore) Purge(ctx context.Context, olderThan time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM chat_transcripts WHERE created_at < ?", olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("transcript: purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
