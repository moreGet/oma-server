-- +goose Up
CREATE TABLE IF NOT EXISTS chat_sessions (
    id          VARCHAR(128) NOT NULL PRIMARY KEY,  -- 클라이언트 생성 세션 ID
    owner_id    VARCHAR(36)  NOT NULL,              -- 소유 멤버 ID
    title       VARCHAR(255) NOT NULL DEFAULT '',
    data_json   TEXT         NOT NULL,              -- 클라이언트 히스토리 JSON(서버 불투명)
    created_at  BIGINT       NOT NULL,              -- unix seconds
    updated_at  BIGINT       NOT NULL
);
CREATE INDEX idx_chat_sessions_owner_id ON chat_sessions(owner_id);
-- +goose Down
DROP TABLE IF EXISTS chat_sessions;
