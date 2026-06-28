-- +goose Up
-- 메시지 수정/삭제: edited_at(0=원본), deleted_at(0=정상, >0=소프트 삭제 시각 + content 비움).
ALTER TABLE chat_messages ADD COLUMN edited_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE chat_messages ADD COLUMN deleted_at BIGINT NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE chat_messages DROP COLUMN deleted_at;
ALTER TABLE chat_messages DROP COLUMN edited_at;
