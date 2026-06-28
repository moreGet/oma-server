-- +goose Up
-- 사용자 간 실시간 채팅(단체/1:1). LLM chat 과 무관한 사람↔사람 메시징.
CREATE TABLE IF NOT EXISTS chat_rooms (
    id          VARCHAR(36)  NOT NULL PRIMARY KEY,
    type        VARCHAR(16)  NOT NULL DEFAULT 'group', -- group | direct
    name        VARCHAR(255),                          -- group 표시명(direct 는 비움)
    direct_key  VARCHAR(80),                           -- direct 방 정준 키(min:max member id) — 1:1 중복 생성 방지
    created_by  VARCHAR(36),
    created_at  BIGINT       NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_chat_rooms_direct_key ON chat_rooms(direct_key);

CREATE TABLE IF NOT EXISTS chat_room_members (
    room_id    VARCHAR(36) NOT NULL,
    member_id  VARCHAR(36) NOT NULL,
    joined_at  BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (room_id, member_id)
);
CREATE INDEX idx_chat_room_members_member ON chat_room_members(member_id);

CREATE TABLE IF NOT EXISTS chat_messages (
    id         VARCHAR(36)  NOT NULL PRIMARY KEY,
    room_id    VARCHAR(36)  NOT NULL,
    sender_id  VARCHAR(36)  NOT NULL,
    content    TEXT         NOT NULL,
    created_at BIGINT       NOT NULL DEFAULT 0
);
CREATE INDEX idx_chat_messages_room ON chat_messages(room_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS chat_messages;
DROP TABLE IF EXISTS chat_room_members;
DROP TABLE IF EXISTS chat_rooms;
