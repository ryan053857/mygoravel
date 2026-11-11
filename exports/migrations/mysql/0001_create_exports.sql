-- +goose Up
-- 后台导出：导出的进度和生成的文件。tenant 为空字符串时是平台本身。
-- status 是 pending、running、completed 或 failed；total_rows 是预计的行数，0 表示不知道。
-- url 是 completed 时文件的公开地址，文件在 expires_at 之后被回收。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据；其他状态为空字符串。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE exports (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    owner_type VARCHAR(191) NOT NULL,
    owner_id VARCHAR(191) NOT NULL,
    kind VARCHAR(191) NOT NULL,
    format VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    total_rows INT NOT NULL,
    processed_rows INT NOT NULL,
    file_id CHAR(36) NULL,
    filename TEXT NOT NULL,
    url TEXT NOT NULL,
    expires_at DATETIME(6) NULL,
    error VARCHAR(191) NOT NULL,
    error_data JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    completed_at DATETIME(6) NULL,
    INDEX exports_owner (tenant, owner_type, owner_id, id),
    INDEX exports_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE exports;
