-- +goose Up
-- 채팅 파일 첨부 바이너리 저장(메타데이터 + BLOB). MEDIUMBLOB = mysql 16MB / sqlite BLOB.
CREATE TABLE IF NOT EXISTS chat_attachments (
    id           VARCHAR(36)  NOT NULL PRIMARY KEY,
    file_name    VARCHAR(255) NOT NULL,
    content_type VARCHAR(128) NOT NULL,
    size_bytes   BIGINT       NOT NULL DEFAULT 0,
    uploader_id  VARCHAR(36),
    created_at   BIGINT       NOT NULL DEFAULT 0,
    data         MEDIUMBLOB   NOT NULL
);
-- +goose Down
DROP TABLE IF EXISTS chat_attachments;
