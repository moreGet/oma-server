-- +goose Up
-- 어드민 멤버 목록(GET /admin/members)은 created_at DESC 로 정렬+LIMIT/OFFSET 한다.
-- 인덱스가 없으면 members 전체를 filesort 하므로, 테이블이 커지기 전에 추가한다.
CREATE INDEX IF NOT EXISTS idx_members_created_at ON members(created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_members_created_at;
