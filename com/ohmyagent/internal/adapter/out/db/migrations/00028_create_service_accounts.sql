-- +goose Up
-- 서비스 계정(사람과 구분되는 비대화형 계정). 별도 id 공간(UUID v4). member 로 흡수하지 않음.
CREATE TABLE IF NOT EXISTS service_accounts (
    id              VARCHAR(36)   NOT NULL PRIMARY KEY, -- UUID v4
    name            VARCHAR(255)  NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    owner_member_id VARCHAR(36)   NOT NULL,             -- 폐기 책임자(members.id, FK 미강제: 멤버 삭제 이력 대비)
    created_at      BIGINT        NOT NULL,             -- unix epoch seconds
    updated_at      BIGINT        NOT NULL,
    created_by      VARCHAR(36),                        -- 생성 admin actor id (NULL=시스템)
    revoked_at      BIGINT        NOT NULL DEFAULT 0    -- 0 = 활성(sentinel)
);
CREATE INDEX idx_service_accounts_owner ON service_accounts(owner_member_id);

-- 장수 API 키. 서버는 SHA-256 해시만 보관(평문 미저장). token_hash 로 O(1) 인증 조회.
CREATE TABLE IF NOT EXISTS service_account_keys (
    id           VARCHAR(36) NOT NULL PRIMARY KEY,      -- UUID v4 (= key_id)
    sa_id        VARCHAR(36) NOT NULL,                  -- service_accounts.id
    token_hash   VARCHAR(64) NOT NULL UNIQUE,           -- SHA-256 hex(64자). 해시 충돌·중복 방지
    created_at   BIGINT      NOT NULL,
    expires_at   BIGINT      NOT NULL DEFAULT 0,        -- 0 = 무기한(sentinel)
    last_used_at BIGINT      NOT NULL DEFAULT 0,        -- 0 = 미사용
    revoked_at   BIGINT      NOT NULL DEFAULT 0,        -- 0 = 활성
    created_by   VARCHAR(36)
);
CREATE INDEX idx_service_account_keys_sa ON service_account_keys(sa_id); -- 계정별 키 목록/일괄폐기
-- token_hash 는 위 UNIQUE 제약이 곧 인증 조회용 인덱스.

-- +goose Down
DROP TABLE IF EXISTS service_account_keys;
DROP TABLE IF EXISTS service_accounts;
