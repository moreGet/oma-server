package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	domainmessaging "aiagent/com/ohmyagent/internal/domain/messaging"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainmessaging.AttachmentStore = (*ChatAttachmentStore)(nil)

// ChatAttachmentStore 는 채팅 첨부 바이너리를 chat_attachments 테이블(BLOB)에 저장한다.
// 향후 파일/S3 백엔드가 필요하면 AttachmentStore 를 다른 구현으로 교체하면 된다.
type ChatAttachmentStore struct {
	db *sql.DB
}

// NewChatAttachmentStore 는 ChatAttachmentStore 를 생성한다.
func NewChatAttachmentStore(conn *sql.DB) *ChatAttachmentStore {
	return &ChatAttachmentStore{db: conn}
}

// Put 은 첨부 메타데이터 + 바이너리를 저장한다.
func (s *ChatAttachmentStore) Put(ctx context.Context, a domainmessaging.StoredAttachment, data []byte) error {
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO chat_attachments (id, file_name, content_type, size_bytes, uploader_id, created_at, data) VALUES (?,?,?,?,?,?,?)",
		a.ID, a.FileName, a.ContentType, a.SizeBytes, nullString(a.UploaderID), a.CreatedAt, data,
	); err != nil {
		return fmt.Errorf("attachment: put: %w", err)
	}
	return nil
}

// attachmentChunkBytes 는 BLOB 을 나눠 읽는 단위다.
//
// 다운로드 1건이 동시에 점유하는 메모리 상한이자, 쿼리 횟수의 반비례 인자다(10MiB 첨부 = 10회).
// 핵심은 점유량이 **파일 크기와 무관해진다**는 것이다 — 전량 적재는 파일크기 × 동시접속에
// 비례해 무한히 커지지만, 청크 방식은 청크 × 동시접속으로 묶인다.
//
// 트레이드오프(실측, 8MiB × 12 동시 다운로드, 로컬):
//   - peak 힙: 41.1 MiB → 30.7 MiB
//   - 소요 시간: 0.32s → 1.18s (쿼리가 파일당 8 회로 늘어 sqlite 단일 커넥션에서 직렬화)
//
// 로컬 벤치는 소비자가 즉시 읽어가므로 이득이 작게 보인다. 실제 이득은 느린 클라이언트가
// 전송 내내 버퍼를 붙잡는 경우다 — 전량 적재면 그동안 파일 전체가 살아있고, 이것이
// 대용량 첨부 + 다수 동시 다운로드에서 OOM 으로 이어지는 경로다.
const attachmentChunkBytes = 1 << 20 // 1 MiB

// Open 은 첨부 메타데이터와 바이너리 스트림을 반환한다(없으면 ErrAttachmentNotFound).
//
// 메타데이터 조회에서 data 컬럼을 **제외**하는 것이 핵심이다 — 예전에는 메타데이터를 얻으려고
// BLOB 전체를 함께 스캔했다. 바이너리는 반환된 리더가 청크 단위로 지연 조회한다.
func (s *ChatAttachmentStore) Open(ctx context.Context, id string) (domainmessaging.StoredAttachment, io.ReadCloser, error) {
	var (
		a        domainmessaging.StoredAttachment
		uploader sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT id, file_name, content_type, size_bytes, uploader_id, created_at FROM chat_attachments WHERE id=?", id).
		Scan(&a.ID, &a.FileName, &a.ContentType, &a.SizeBytes, &uploader, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmessaging.StoredAttachment{}, nil, domainmessaging.ErrAttachmentNotFound
	}
	if err != nil {
		return domainmessaging.StoredAttachment{}, nil, fmt.Errorf("attachment: open: %w", err)
	}
	a.UploaderID = uploader.String

	return a, &attachmentBlobReader{
		ctx:    ctx,
		db:     s.db,
		id:     id,
		offset: 1, // substr 은 1-base
		remain: a.SizeBytes,
	}, nil
}

// attachmentBlobReader 는 BLOB 을 청크 단위로 지연 조회하는 io.ReadCloser 다.
// 파일 크기와 무관하게 청크 하나 분량의 메모리만 점유한다.
//
// substr(data, offset, len) 은 SQLite·MySQL 양쪽에서 **바이트 단위**로 동작하므로
// 드라이버 분기가 필요 없다(둘 다 BLOB/binary string 을 바이트로 다룬다).
type attachmentBlobReader struct {
	ctx    context.Context
	db     *sql.DB
	id     string
	offset int64  // 다음에 읽을 위치(1-base)
	remain int64  // 남은 바이트
	buf    []byte // 현재 청크
	pos    int    // buf 내 소비 위치
}

func (r *attachmentBlobReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.buf) {
		if r.remain <= 0 {
			return 0, io.EOF
		}
		n := int64(attachmentChunkBytes)
		if n > r.remain {
			n = r.remain
		}
		var chunk []byte
		err := r.db.QueryRowContext(r.ctx,
			"SELECT substr(data, ?, ?) FROM chat_attachments WHERE id=?", r.offset, n, r.id).Scan(&chunk)
		if errors.Is(err, sql.ErrNoRows) {
			// 스트리밍 도중 삭제된 경우.
			return 0, domainmessaging.ErrAttachmentNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("attachment: read chunk at %d: %w", r.offset, err)
		}
		if len(chunk) == 0 {
			// size_bytes 가 실제 BLOB 보다 큰 경우(데이터 불일치) 무한 루프를 막는다.
			return 0, io.EOF
		}
		r.buf = chunk
		r.pos = 0
		r.offset += int64(len(chunk))
		r.remain -= int64(len(chunk))
	}
	n := copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}

func (r *attachmentBlobReader) Close() error {
	r.buf = nil
	return nil
}

// Stats 는 첨부 개수와 총 바이트 수를 반환한다(어드민 집계).
func (s *ChatAttachmentStore) Stats(ctx context.Context) (int, int64, error) {
	var (
		count int
		bytes int64
	)
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM chat_attachments").Scan(&count, &bytes); err != nil {
		return 0, 0, fmt.Errorf("attachment: stats: %w", err)
	}
	return count, bytes, nil
}
