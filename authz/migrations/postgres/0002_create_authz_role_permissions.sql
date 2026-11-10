-- +goose Up
CREATE TABLE authz_role_permissions (
    role_id uuid NOT NULL,
    permission text NOT NULL,
    PRIMARY KEY (role_id, permission)
);
COMMENT ON TABLE authz_role_permissions IS '角色包含的权限，permission 可以以 :* 结尾或为 *';

-- +goose Down
DROP TABLE authz_role_permissions;
