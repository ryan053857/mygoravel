-- +goose Up
-- webhook 端点：租户配置的接收地址。tenant 为空字符串时是平台本身。
-- events 是订阅的事件类型：完整类型、以 .* 结尾的前缀或 *。
-- previous_secret 是轮换前的密钥，在 previous_secret_expires_at 之前仍参与签名；没有时为空字符串。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE webhook_endpoints (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    url TEXT NOT NULL,
    description TEXT NOT NULL,
    events JSON NOT NULL,
    secret VARCHAR(255) NOT NULL,
    disabled BOOLEAN NOT NULL,
    previous_secret VARCHAR(255) NOT NULL,
    previous_secret_expires_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    INDEX webhook_endpoints_tenant (tenant, id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE webhook_endpoints;
