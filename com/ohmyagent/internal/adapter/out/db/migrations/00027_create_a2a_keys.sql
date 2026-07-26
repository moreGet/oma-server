-- +goose Up
-- A2A 토큰 브로커 서명 키(ES256/P-256). 개인키는 Provider api_key 직접 저장과 동일하게
-- AES-GCM(APP_ENCRYPTION_SECRET) 암호문으로만 저장한다. v1 은 단일 활성 키(수동 회전).
CREATE TABLE IF NOT EXISTS a2a_keys (
    kid                       VARCHAR(36) NOT NULL PRIMARY KEY, -- JWT 헤더 kid
    private_key_pem_encrypted TEXT        NOT NULL,             -- PKCS#8 PEM 의 AES-GCM 암호문(평문 저장 금지)
    public_key_pem            TEXT        NOT NULL,             -- SPKI PEM(수신측 검증용, 공개 값)
    active                    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at                BIGINT      NOT NULL              -- unix epoch seconds
);

-- +goose Down
DROP TABLE IF EXISTS a2a_keys;
