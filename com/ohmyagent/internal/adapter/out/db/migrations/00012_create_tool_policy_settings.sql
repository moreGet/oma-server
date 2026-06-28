-- +goose Up
CREATE TABLE IF NOT EXISTS tool_policy_settings (
    id               INT          NOT NULL PRIMARY KEY,   -- 항상 1 (단일 행)
    mode             VARCHAR(16)  NOT NULL DEFAULT 'cached', -- cached | realtime
    enabled          TEXT,                                  -- JSON 문자열 배열(허용 화이트리스트). 빈/NULL = 전체 허용
    disabled         TEXT,                                  -- JSON 문자열 배열(블랙리스트, enabled 보다 우선)
    blocked_patterns TEXT,                                  -- JSON: [{type,pattern,reason,script_type}]
    blocked_paths    TEXT,                                  -- JSON: [{type,pattern,reason}]
    updated_at       BIGINT       NOT NULL DEFAULT 0,
    updated_by       VARCHAR(36)
);
INSERT INTO tool_policy_settings (id, mode) VALUES (1, 'cached');
-- +goose Down
DROP TABLE IF EXISTS tool_policy_settings;
