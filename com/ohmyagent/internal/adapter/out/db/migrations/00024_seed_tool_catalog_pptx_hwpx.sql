-- +goose Up
-- 카탈로그 드리프트 보정: 클라이언트에 PPTX·HWPX 도구 3개가 추가됐으나(커밋 007250c)
-- 카탈로그가 함께 갱신되지 않아, 어드민이 이 도구들을 정책으로 통제할 수 없는 상태였다.
-- IsKnownTool 이 카탈로그로 정책 입력을 검증하므로 목록에 없으면 enabled/disabled 에 넣을 수 없다.
-- 00023(1..29)에 이어 sort_order 30..32 로 추가한다. catalog.go(ClientTools)와 동기화.
INSERT INTO tool_catalog (name, category, sort_order) VALUES
    ('read_pptx',  '문서·데이터', 30),
    ('write_pptx', '문서·데이터', 31),
    ('read_hwpx',  '문서·데이터', 32);

-- +goose Down
DELETE FROM tool_catalog WHERE name IN ('read_pptx', 'write_pptx', 'read_hwpx');
