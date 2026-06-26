-- +goose Up
CREATE TABLE IF NOT EXISTS token_usage (
    member_id   VARCHAR(36) NOT NULL,
    period      VARCHAR(10) NOT NULL,           -- 일=YYYY-MM-DD / 주=YYYY-Www / 월=YYYY-MM (UTC)
    used_tokens BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (member_id, period)
);
CREATE INDEX idx_token_usage_period ON token_usage(period);

CREATE TABLE IF NOT EXISTS member_token_limits (
    member_id     VARCHAR(36) NOT NULL PRIMARY KEY,
    daily_limit   BIGINT      NOT NULL DEFAULT 0,  -- 0 = 전역 기본값 사용
    weekly_limit  BIGINT      NOT NULL DEFAULT 0,
    monthly_limit BIGINT      NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS quota_config (
    id                    INT    NOT NULL PRIMARY KEY,  -- 항상 1
    default_daily_limit   BIGINT NOT NULL DEFAULT 0,    -- 0 = 무제한
    default_weekly_limit  BIGINT NOT NULL DEFAULT 0,
    default_monthly_limit BIGINT NOT NULL DEFAULT 0
);
INSERT INTO quota_config (id, default_daily_limit, default_weekly_limit, default_monthly_limit) VALUES (1, 0, 0, 0);
-- +goose Down
DROP TABLE IF EXISTS quota_config;
DROP TABLE IF EXISTS member_token_limits;
DROP TABLE IF EXISTS token_usage;
