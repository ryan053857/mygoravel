-- +goose Up
-- 角色包含的权限，permission 可以以 :* 结尾或为 *。
CREATE TABLE authz_role_permissions (
    role_id CHAR(36) NOT NULL,
    permission VARCHAR(191) NOT NULL,
    PRIMARY KEY (role_id, permission)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE authz_role_permissions;
