-- +goose Up
-- 클라이언트가 노출하는 도구 카탈로그(어드민 도구 정책 칩의 단일 진실원).
-- 어드민이 enabled/disabled 를 자유 문자열로 입력하다 생기는 오타를 막기 위해
-- 고정된 도구명 목록을 미리 시드한다. sort_order = App.xaml.cs tools[] 등록(노출) 순서.
-- sync with client App.xaml.cs tools[] / domain/toolpolicy/catalog.go (ClientTools)
CREATE TABLE IF NOT EXISTS tool_catalog (
    name       VARCHAR(64)  NOT NULL PRIMARY KEY, -- 와이어 도구명(소문자 snake_case, 정책 매칭 키)
    category   VARCHAR(32)  NOT NULL,             -- 셸 | 파일 | 시스템 | 문서·데이터
    sort_order INT          NOT NULL              -- 노출 순서(1..26)
);
INSERT INTO tool_catalog (name, category, sort_order) VALUES
    ('run_command',              '셸',          1),
    ('read_file',                '파일',        2),
    ('write_file',               '파일',        3),
    ('edit_file',                '파일',        4),
    ('list_directory',           '파일',        5),
    ('glob',                     '파일',        6),
    ('grep',                     '파일',        7),
    ('create_directory',         '파일',        8),
    ('move',                     '파일',        9),
    ('copy',                     '파일',        10),
    ('delete',                   '파일',        11),
    ('get_environment',          '시스템',      12),
    ('clipboard_read',           '시스템',      13),
    ('clipboard_write',          '시스템',      14),
    ('list_processes',           '시스템',      15),
    ('list_processes_memory_kb', '시스템',      16),
    ('start_process',            '시스템',      17),
    ('kill_process',             '시스템',      18),
    ('http_fetch',               '시스템',      19),
    ('screenshot',               '시스템',      20),
    ('read_csv',                 '문서·데이터', 21),
    ('write_csv',                '문서·데이터', 22),
    ('read_excel',               '문서·데이터', 23),
    ('write_excel',              '문서·데이터', 24),
    ('read_pdf',                 '문서·데이터', 25),
    ('read_document',            '문서·데이터', 26);
-- +goose Down
DROP TABLE IF EXISTS tool_catalog;
