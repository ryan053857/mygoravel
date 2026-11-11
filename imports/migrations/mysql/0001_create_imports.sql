-- +goose Up
-- 后台导入：一个上传的表格文件和它的处理进度。tenant 为空字符串时是平台本身。
-- status 是 pending、running、completed 或 failed；last_line 是已经处理到的行号，任务重试时从下一行继续。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据；其他状态为空字符串。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致；
-- 参与索引的字符串列为 VARCHAR(191)，使组合索引不超过 InnoDB 的 3072 字节上限。
CREATE TABLE imports (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    owner_type VARCHAR(191) NOT NULL,
    owner_id VARCHAR(191) NOT NULL,
    kind VARCHAR(191) NOT NULL,
    file_id CHAR(36) NOT NULL,
    filename TEXT NOT NULL,
    locale VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL,
    header JSON NOT NULL,
    total_rows INT NOT NULL,
    processed_rows INT NOT NULL,
    failed_rows INT NOT NULL,
    last_line INT NOT NULL,
    error VARCHAR(191) NOT NULL,
    error_data JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    completed_at DATETIME(6) NULL,
    INDEX imports_owner (tenant, owner_type, owner_id, id),
    INDEX imports_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE imports;
