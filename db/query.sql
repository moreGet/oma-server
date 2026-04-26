-- name: GetActiveProvider :one
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
WHERE is_active = 1
LIMIT 1;

-- name: GetProviderByID :one
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
WHERE id = ?;

-- name: ListProviders :many
SELECT id, name, is_active, provider_type, config_json, created_at, updated_at
FROM llm_providers
ORDER BY id ASC;

-- name: CreateProvider :execresult
INSERT INTO llm_providers (name, is_active, provider_type, config_json)
VALUES (?, ?, ?, ?);

-- name: DeactivateAllProviders :exec
UPDATE llm_providers SET is_active = 0 WHERE is_active = 1;

-- name: ActivateProviderByID :execrows
UPDATE llm_providers SET is_active = 1 WHERE id = ?;

-- name: UpdateProviderConfig :exec
UPDATE llm_providers SET config_json = ? WHERE id = ?;

-- name: DeleteProvider :execrows
DELETE FROM llm_providers WHERE id = ?;
