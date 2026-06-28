-- +goose Up
-- 클라이언트 버전 점검(GET /api/v1/client/version) 설정. 어드민 /admin/client 에서 편집.
CREATE TABLE IF NOT EXISTS client_version_settings (
    id                INT          NOT NULL PRIMARY KEY,   -- 항상 1 (단일 행)
    latest            VARCHAR(64)  NOT NULL DEFAULT '',
    minimum_supported VARCHAR(64)  NOT NULL DEFAULT '',
    download_url      VARCHAR(512) NOT NULL DEFAULT '',
    notice            VARCHAR(512) NOT NULL DEFAULT '',
    mandatory         BOOLEAN      NOT NULL DEFAULT FALSE,
    updated_at        BIGINT       NOT NULL DEFAULT 0,
    updated_by        VARCHAR(36)
);
INSERT INTO client_version_settings (id, latest, minimum_supported) VALUES (1, '1.0.0', '1.0.0');
-- +goose Down
DROP TABLE IF EXISTS client_version_settings;
