-- docker-compose 최초 기동 시 자동 실행되는 초기화 스크립트
-- (migrations/*.up.sql 내용을 통합)

CREATE TABLE IF NOT EXISTS llm_providers (
    id            BIGINT         NOT NULL AUTO_INCREMENT,
    name          VARCHAR(100)   NOT NULL,
    is_active     TINYINT(1)     NOT NULL DEFAULT 0,
    provider_type ENUM('LOCAL','EXTERNAL') NOT NULL,
    config_json   JSON           NOT NULL,
    created_at    TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    INDEX idx_is_active (is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 로컬 테스트용 샘플 데이터
INSERT IGNORE INTO llm_providers (name, is_active, provider_type, config_json) VALUES
    ('Ollama-local', 1, 'LOCAL',    '{"endpoint":"http://localhost:11434","model":"llama3"}'),
    ('Claude-3',     0, 'EXTERNAL', '{"model":"claude-3-5-sonnet-20241022","api_key_env":"ANTHROPIC_API_KEY"}'),
    ('GPT-4o',       0, 'EXTERNAL', '{"model":"gpt-4o","api_key_env":"OPENAI_API_KEY"}');
