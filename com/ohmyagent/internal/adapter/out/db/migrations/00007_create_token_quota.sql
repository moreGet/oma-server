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
    monthly_limit BIGINT      NOT NULL DEFAULT 0  -- 0 = 전역 기본값 사용
);

CREATE TABLE IF NOT EXISTS quota_config (
    id                    INT    NOT NULL PRIMARY KEY,  -- 항상 1
    default_monthly_limit BIGINT NOT NULL DEFAULT 0     -- 0 = 무제한
);
INSERT INTO quota_config (id, default_monthly_limit) VALUES (1, 0);
-- 참고: 일/주 한도 컬럼(daily/weekly)은 00010 에서 ALTER 로 추가(기존 DB 호환).
-- +goose Down
DROP TABLE IF EXISTS quota_config;
DROP TABLE IF EXISTS member_token_limits;
DROP TABLE IF EXISTS token_usage;
