-- +goose Up
-- 서브에이전트 도구(task) 추가. 클라이언트가 격리된 읽기 전용 하위 에이전트 루프를 돌려 결론만 회수한다.
-- 어드민이 이 도구를 정책으로 끌 수 있어야 하므로 카탈로그에 등록한다
-- (끄면 IsExposed=false → 모델에게 스키마 자체가 안 보인다).
-- 00024(1..32)에 이어 sort_order 33. catalog.go(ClientTools)와 동기화.
INSERT INTO tool_catalog (name, category, sort_order) VALUES
    ('task', '에이전트', 33);

-- +goose Down
DELETE FROM tool_catalog WHERE name = 'task';
