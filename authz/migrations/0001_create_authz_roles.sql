-- +goose Up
CREATE TABLE authz_roles (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    name text NOT NULL,
    description text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT authz_roles_tenant_name_unique UNIQUE (tenant, name)
);
COMMENT ON TABLE authz_roles IS '角色：一组权限';
COMMENT ON COLUMN authz_roles.tenant IS '空字符串为全局角色，可在任何租户和平台中分配；否则是该租户自定义的角色';

-- +goose Down
DROP TABLE authz_roles;
