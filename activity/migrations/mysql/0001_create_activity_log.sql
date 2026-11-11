-- +goose Up
-- 操作日志：谁（actor）在哪个租户中对什么（target）做了什么（event）。
-- tenant 为空字符串时是平台本身；actor_id 为空字符串表示没有操作人。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE activity_log (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    actor_type VARCHAR(191) NOT NULL,
    actor_id VARCHAR(191) NOT NULL,
    event VARCHAR(191) NOT NULL,
    target_type VARCHAR(191) NOT NULL,
    target_id VARCHAR(191) NOT NULL,
    properties JSON NOT NULL,
    request_id VARCHAR(191) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    INDEX activity_log_tenant (tenant, id),
    INDEX activity_log_target (tenant, target_type, target_id, id),
    INDEX activity_log_actor (tenant, actor_id, id),
    INDEX activity_log_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE activity_log;
