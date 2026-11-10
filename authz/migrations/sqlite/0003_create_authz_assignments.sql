-- +goose Up
-- 主体在某个域中拥有的角色。tenant 为空字符串时是平台本身，* 为所有租户，其余为租户 ID。
CREATE TABLE authz_assignments (
    subject_id TEXT NOT NULL,
    tenant TEXT NOT NULL,
    role_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (subject_id, tenant, role_id)
);
CREATE INDEX authz_assignments_tenant ON authz_assignments (tenant);
CREATE INDEX authz_assignments_role_id ON authz_assignments (role_id);

-- +goose Down
DROP TABLE authz_assignments;
