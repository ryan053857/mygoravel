-- +goose Up
-- 已上传文件；公开地址 = 磁盘 public_url + key。disk 是应用装配时的磁盘名，不是驱动名。
-- expires_at：temporary 未确认的回收时间，deleted 等待清理任务删除对象的时间；ready 为空。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE files (
    id CHAR(36) NOT NULL PRIMARY KEY,
    disk VARCHAR(64) NOT NULL,
    `key` VARCHAR(255) NOT NULL,
    filename TEXT NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    size BIGINT NOT NULL CHECK (size >= 0),
    owner_type VARCHAR(64) NOT NULL,
    owner_id VARCHAR(255) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('temporary', 'ready', 'deleted')),
    expires_at DATETIME(6),
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    CONSTRAINT files_disk_key_unique UNIQUE (disk, `key`),
    INDEX files_owner (owner_type, owner_id),
    INDEX files_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE files;
