-- +goose Up
CREATE TABLE authz_assignments (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    tenant text NOT NULL,
    role_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (subject_type, subject_id, tenant, role_id)
);
CREATE INDEX authz_assignments_tenant ON authz_assignments (tenant);
CREATE INDEX authz_assignments_role_id ON authz_assignments (role_id);
COMMENT ON TABLE authz_assignments IS '主体在某个域中拥有的角色';
COMMENT ON COLUMN authz_assignments.tenant IS '空字符串为平台本身，* 为所有租户，其余为租户 ID';

-- +goose Down
DROP TABLE authz_assignments;
