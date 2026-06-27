-- +goose Up
CREATE TABLE IF NOT EXISTS transcript_settings (
    id            INT          NOT NULL PRIMARY KEY,   -- 항상 1 (단일 행)
    enabled       BOOLEAN      NOT NULL DEFAULT TRUE,
    backend       VARCHAR(16)  NOT NULL DEFAULT 'db',  -- db | file | s3
    file_dir      VARCHAR(512),
    s3_endpoint   VARCHAR(256),
    s3_bucket     VARCHAR(256),
    s3_region     VARCHAR(64),
    s3_access_key VARCHAR(256),
    s3_secret_key VARCHAR(512),                        -- AES-GCM 암호문
    s3_use_ssl    BOOLEAN      NOT NULL DEFAULT TRUE,
    updated_at    BIGINT       NOT NULL DEFAULT 0,
    updated_by    VARCHAR(36)
);
INSERT INTO transcript_settings (id, enabled, backend, s3_use_ssl) VALUES (1, TRUE, 'db', TRUE);
-- 참고: retention_days·strip_attachments 컬럼은 00011 에서 ALTER 로 추가(기존 DB 호환).
-- +goose Down
DROP TABLE IF EXISTS transcript_settings;
