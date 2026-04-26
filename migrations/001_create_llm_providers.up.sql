-- MariaDB 10.x 호환
-- LLM 제공자 메타데이터.
-- is_active=1 인 행은 항상 정확히 0개 또는 1개여야 한다 (애플리케이션 트랜잭션으로 보장).
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
