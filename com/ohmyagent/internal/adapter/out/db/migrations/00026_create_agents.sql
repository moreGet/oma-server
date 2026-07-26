-- +goose Up
-- 에이전트 레지스트리(등록·발견·생존성). status 는 저장하지 않고 read 시 last_heartbeat_at 로 계산한다.
-- capabilities/tags 는 json text 로 저장하고 발견 필터는 앱단에서 정밀 판정한다(v1).
--   트레이드오프: 정규화 테이블(agent_capabilities) + 인덱스면 SQL 로 정확 필터가 되지만,
--   에이전트 수는 멤버 수 규모(수백~수천)라 전량 스캔 + 앱단 필터로 충분하다.
--   수만 대 규모가 되면 정규화로 전환(새 마이그레이션)한다.
CREATE TABLE IF NOT EXISTS agents (
    id                VARCHAR(36)   NOT NULL PRIMARY KEY,  -- 서버 발급 UUID(agent_id)
    owner_member_id   VARCHAR(36)   NOT NULL,              -- 등록한 멤버(JWT). members.id 참조(어드민 강제 해제·멤버 삭제 이력 대비 FK 미강제)
    name              VARCHAR(255)  NOT NULL,
    endpoint_url      VARCHAR(2048) NOT NULL,              -- A2A 리스너 base URL
    capabilities      TEXT          NOT NULL,              -- json 배열(예: ["code-review"])
    tags              TEXT          NOT NULL,              -- json 배열(예: ["prod","gpu"])
    model             VARCHAR(255)  NOT NULL DEFAULT '',
    version           VARCHAR(64)   NOT NULL DEFAULT '',
    last_heartbeat_at BIGINT        NOT NULL DEFAULT 0,    -- unix epoch seconds
    created_at        BIGINT        NOT NULL,
    updated_at        BIGINT        NOT NULL,
    UNIQUE (owner_member_id, name)                          -- 재등록 업서트 키(agent_id 유지)
);
CREATE INDEX idx_agents_owner ON agents(owner_member_id);
-- 발견/청소 성능: 기본 발견은 online+stale(= 최근 heartbeat)만 반환하고 sweeper 는 오래된 행을 지운다.
CREATE INDEX idx_agents_last_heartbeat ON agents(last_heartbeat_at);

-- +goose Down
DROP TABLE IF EXISTS agents;
