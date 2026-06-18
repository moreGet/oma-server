-- +goose Up
CREATE TABLE IF NOT EXISTS llm_providers (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,   -- UUID v4
    name          VARCHAR(100) NOT NULL,
    is_active     BOOLEAN      NOT NULL DEFAULT FALSE,
    provider_type VARCHAR(20)  NOT NULL,               -- ENUM → VARCHAR(sqlite 호환)
    config_json   TEXT         NOT NULL,               -- JSON → TEXT(app 에서 JSON 직렬화)
    created_at    BIGINT       NOT NULL,
    updated_at    BIGINT       NOT NULL,
    created_by    VARCHAR(36),
    updated_by    VARCHAR(36)
);
CREATE INDEX idx_llm_providers_is_active ON llm_providers(is_active);
-- +goose Down
DROP TABLE IF EXISTS llm_providers;
