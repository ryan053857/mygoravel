-- +goose Up
-- 用户在后台发起的长操作，例如导入、导出和批量处理，记录它的状态和进度。tenant 为空字符串时是平台本身。
-- kind 由应用定义，例如 articles.import；status 是 pending、running、completed 或 failed。
-- total 是预计的单位数，0 表示还不知道；processed 是已经处理的单位数，包括失败的单位。
-- version 每次保存加一，同一个操作的两次执行据此发现对方。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据；其他状态为空字符串。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE operations (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    owner_type VARCHAR(191) NOT NULL,
    owner_id VARCHAR(191) NOT NULL,
    kind VARCHAR(191) NOT NULL,
    status VARCHAR(16) NOT NULL,
    total INT NOT NULL,
    processed INT NOT NULL,
    failed INT NOT NULL,
    version INT NOT NULL,
    error VARCHAR(191) NOT NULL,
    error_data JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    completed_at DATETIME(6) NULL,
    INDEX operations_owner (tenant, owner_type, owner_id, id),
    INDEX operations_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE operations;
