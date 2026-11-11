-- +goose Up
-- 功能开关的设置：全员（tenant 与 subject_id 都为空字符串）、某个租户或某个用户。
-- mode 为 on、off，或 percent（只用于全员）。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE feature_settings (
    name VARCHAR(191) NOT NULL,
    tenant VARCHAR(191) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    mode VARCHAR(16) NOT NULL,
    percent SMALLINT NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (name, tenant, subject_id),
    INDEX feature_settings_scope (tenant, subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE feature_settings;
