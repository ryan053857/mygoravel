-- +goose Up
-- 主体在某个域中拥有的角色。tenant 为空字符串时是平台本身，* 为所有租户，其余为租户 ID。
CREATE TABLE authz_assignments (
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    tenant VARCHAR(191) NOT NULL,
    role_id CHAR(36) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (subject_type, subject_id, tenant, role_id),
    INDEX authz_assignments_tenant (tenant),
    INDEX authz_assignments_role_id (role_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE authz_assignments;
