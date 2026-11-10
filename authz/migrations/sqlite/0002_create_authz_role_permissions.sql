-- +goose Up
-- 角色包含的权限，permission 可以以 :* 结尾或为 *。
CREATE TABLE authz_role_permissions (
    role_id TEXT NOT NULL,
    permission TEXT NOT NULL,
    PRIMARY KEY (role_id, permission)
);

-- +goose Down
DROP TABLE authz_role_permissions;
