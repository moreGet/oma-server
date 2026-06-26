-- +goose Up
CREATE TABLE IF NOT EXISTS chat_transcripts (
    id                VARCHAR(36)  NOT NULL PRIMARY KEY,  -- UUID v4
    member_id         VARCHAR(36),                        -- 질의자(NULL 가능)
    session_id        VARCHAR(36),                        -- 선택(agent 세션)
    source            VARCHAR(16)  NOT NULL,              -- chat | agent
    model             VARCHAR(128),
    prompt_tokens     INT          NOT NULL DEFAULT 0,
    completion_tokens INT          NOT NULL DEFAULT 0,
    total_tokens      INT          NOT NULL DEFAULT 0,
    finish_reason     VARCHAR(32),
    content           BLOB,                               -- gzip(JSON{request,response})
    created_at        BIGINT       NOT NULL               -- unix seconds
);
CREATE INDEX idx_chat_transcripts_member ON chat_transcripts(member_id);
CREATE INDEX idx_chat_transcripts_created ON chat_transcripts(created_at);
-- +goose Down
DROP TABLE IF EXISTS chat_transcripts;
