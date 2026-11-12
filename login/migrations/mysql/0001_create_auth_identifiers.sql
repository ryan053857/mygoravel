-- +goose Up
-- 登录标识，由 login.Realm 管理：kind 为 email、phone、username 或第三方登录的提供方名称，value 是规范化后的值；
-- Realm 为 PerTenant 时标识在租户内唯一，否则 tenant 为空字符串；verified_at 为空表示未验证。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE auth_identifiers (
    subject_type VARCHAR(64) NOT NULL,
    tenant VARCHAR(255) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    value VARCHAR(191) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    verified_at DATETIME(6),
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (subject_type, tenant, kind, value),
    INDEX auth_identifiers_subject (subject_type, subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_identifiers;
