-- +goose Up
ALTER TABLE members ADD COLUMN email VARCHAR(255);
ALTER TABLE members ADD COLUMN display_name VARCHAR(255);
ALTER TABLE members ADD COLUMN organization VARCHAR(255);
-- +goose Down
ALTER TABLE members DROP COLUMN organization;
ALTER TABLE members DROP COLUMN display_name;
ALTER TABLE members DROP COLUMN email;
