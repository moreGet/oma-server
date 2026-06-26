-- +goose Up
CREATE TABLE IF NOT EXISTS projects (
    id          VARCHAR(36)  NOT NULL PRIMARY KEY,
    owner_id    VARCHAR(36)  NOT NULL,
    client_id   VARCHAR(64)  NOT NULL,            -- 클라 로컬 GUID(업서트 키)
    name        VARCHAR(255) NOT NULL,
    created_utc BIGINT       NOT NULL DEFAULT 0,
    updated_utc BIGINT       NOT NULL DEFAULT 0,
    UNIQUE (owner_id, client_id)
);
CREATE INDEX idx_projects_owner ON projects(owner_id);

CREATE TABLE IF NOT EXISTS conversations (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,
    project_id    VARCHAR(36),                     -- NULL = 미분류
    owner_id      VARCHAR(36)  NOT NULL,
    client_id     VARCHAR(64)  NOT NULL,           -- 클라 세션 GUID(업서트 키)
    title         VARCHAR(255) NOT NULL,
    created_utc   BIGINT       NOT NULL DEFAULT 0,
    updated_utc   BIGINT       NOT NULL DEFAULT 0,
    message_count INT          NOT NULL DEFAULT 0,
    UNIQUE (owner_id, client_id)
);
CREATE INDEX idx_conversations_owner ON conversations(owner_id);
CREATE INDEX idx_conversations_project ON conversations(project_id);

-- DB 백엔드용 대화 본문(gzip). 파일/S3 백엔드는 외부 저장.
CREATE TABLE IF NOT EXISTS session_blobs (
    blob_key VARCHAR(255) NOT NULL PRIMARY KEY,
    content  BLOB         NOT NULL
);

CREATE TABLE IF NOT EXISTS session_settings (
    id                   INT          NOT NULL PRIMARY KEY, -- 항상 1
    backend              VARCHAR(16)  NOT NULL DEFAULT 'db',
    file_dir             VARCHAR(512),
    s3_endpoint          VARCHAR(255),
    s3_bucket            VARCHAR(255),
    s3_region            VARCHAR(64),
    s3_access_key        VARCHAR(255),
    s3_secret_key        VARCHAR(512),                       -- AES-GCM 암호문
    s3_use_ssl           BOOLEAN      NOT NULL DEFAULT TRUE,
    default_max_sessions INT          NOT NULL DEFAULT 0,    -- 0 = 무제한
    updated_at           BIGINT       NOT NULL DEFAULT 0,
    updated_by           VARCHAR(36)
);
INSERT INTO session_settings (id, backend, s3_use_ssl, default_max_sessions) VALUES (1, 'db', TRUE, 0);

CREATE TABLE IF NOT EXISTS member_session_limits (
    member_id    VARCHAR(36) NOT NULL PRIMARY KEY,
    max_sessions INT         NOT NULL DEFAULT 0  -- 0 = 전역 기본값 사용
);
-- +goose Down
DROP TABLE IF EXISTS member_session_limits;
DROP TABLE IF EXISTS session_settings;
DROP TABLE IF EXISTS session_blobs;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS projects;
