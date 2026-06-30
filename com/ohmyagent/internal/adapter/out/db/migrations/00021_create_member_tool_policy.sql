-- +goose Up
-- 멤버별 도구 정책 오버라이드(전역 tool_policy_settings 에 계층 병합되는 멤버 단위 허용/차단).
-- 모드(cached/realtime)는 전역 전용이라 여기 없음. 빈 오버라이드는 행을 두지 않는다(전역만 적용).
-- 행이 없는 멤버는 전역 정책을 그대로 따른다. 멤버 삭제 시 고아 행은 무해(member_id 로만 조회).
CREATE TABLE IF NOT EXISTS member_tool_policy (
    member_id  VARCHAR(36) NOT NULL PRIMARY KEY,
    enabled    TEXT,                          -- JSON 문자열 배열(허용 화이트리스트). 빈/NULL = 오버라이드 없음
    disabled   TEXT,                          -- JSON 문자열 배열(추가 차단). 빈/NULL = 오버라이드 없음
    updated_at BIGINT      NOT NULL DEFAULT 0,
    updated_by VARCHAR(36)
);
-- +goose Down
DROP TABLE IF EXISTS member_tool_policy;
