-- +goose Up
-- 멘션 피드(GET /chat/mentions)는 room 필터 없이 created_at DESC 로 정렬+LIMIT 한다.
-- created_at 인덱스가 없으면 chat_messages 전체 스캔 + filesort 가 발생하므로 추가한다.
-- (인덱스 순서 스캔 + 조기 LIMIT 종료로 최신 멘션 조회 비용을 메시지 총량과 무관하게 만든다.)
CREATE INDEX IF NOT EXISTS idx_chat_messages_created ON chat_messages(created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_chat_messages_created;
