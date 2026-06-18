-- +goose Up
CREATE TABLE IF NOT EXISTS roles (
    id    INT          NOT NULL PRIMARY KEY,   -- 1/2/3
    name  VARCHAR(50)  NOT NULL UNIQUE,        -- user/admin/super_admin
    level INT          NOT NULL                -- 0/1/2 (RoleLevel)
);
INSERT INTO roles (id, name, level) VALUES (1,'user',0),(2,'admin',1),(3,'super_admin',2);
-- +goose Down
DROP TABLE IF EXISTS roles;
