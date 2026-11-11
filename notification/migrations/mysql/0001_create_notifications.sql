-- +goose Up
-- 站内通知：发给某个租户中某个主体（recipient）的通知。
-- tenant 为空字符串时是平台本身；read_at 为 NULL 表示未读。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- MySQL 没有部分索引，未读查询用 (…, read_at, id) 索引。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE notifications (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    recipient_type VARCHAR(191) NOT NULL,
    recipient_id VARCHAR(191) NOT NULL,
    event VARCHAR(191) NOT NULL,
    data JSON NOT NULL,
    read_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    INDEX notifications_recipient (tenant, recipient_type, recipient_id, id),
    INDEX notifications_unread (tenant, recipient_type, recipient_id, read_at, id),
    INDEX notifications_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE notifications;
