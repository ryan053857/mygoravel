-- +goose Up
-- 角色：一组权限。tenant 为空字符串时是全局角色，可在任何租户和平台中分配；否则是该租户自定义的角色。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE authz_roles (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CONSTRAINT authz_roles_tenant_name_unique UNIQUE (tenant, name)
);

-- +goose Down
DROP TABLE authz_roles;
