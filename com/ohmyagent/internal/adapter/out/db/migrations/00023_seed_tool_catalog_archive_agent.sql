-- +goose Up
-- 클라 내장 도구 확장(2026-07-14): 압축 2개 + 에이전트 메타 1개(총 29개).
-- 00020 시드(1..26)에 이어 sort_order 27..29 로 추가한다. catalog.go(ClientTools)와 동기화.
INSERT INTO tool_catalog (name, category, sort_order) VALUES
    ('compress_files',  '압축',     27),
    ('extract_archive', '압축',     28),
    ('manage_todos',    '에이전트', 29);

-- +goose Down
DELETE FROM tool_catalog WHERE name IN ('compress_files', 'extract_archive', 'manage_todos');
