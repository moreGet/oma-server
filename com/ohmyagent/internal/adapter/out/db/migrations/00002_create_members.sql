-- +goose Up
CREATE TABLE IF NOT EXISTS members (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,  -- UUID v4
    username      VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    active        BOOLEAN      NOT NULL DEFAULT TRUE,
    role_id       INT          NOT NULL,
    created_at    BIGINT       NOT NULL,              -- unix seconds
    updated_at    BIGINT       NOT NULL,
    created_by    VARCHAR(36),                        -- NULL=시스템
    updated_by    VARCHAR(36),
    FOREIGN KEY (role_id) REFERENCES roles(id)
);
CREATE INDEX idx_members_role_id ON members(role_id);
-- +goose Down
DROP TABLE IF EXISTS members;
