-- +goose Up
-- 읽음 표시/안읽음 카운트: 멤버가 방을 어디까지 읽었는지(unix sec). 0 = 아무것도 안 읽음.
ALTER TABLE chat_room_members ADD COLUMN last_read_at BIGINT NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE chat_room_members DROP COLUMN last_read_at;
