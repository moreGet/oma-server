-- +goose Up
-- 멘션(JSON 멤버ID 배열) + 파일 첨부 메타데이터(JSON {file_name,content_type,size_bytes,url} 배열).
ALTER TABLE chat_messages ADD COLUMN mentions TEXT;
ALTER TABLE chat_messages ADD COLUMN attachments TEXT;
-- +goose Down
ALTER TABLE chat_messages DROP COLUMN attachments;
ALTER TABLE chat_messages DROP COLUMN mentions;
