-- +goose Up
-- 原子锁；过期行由 lock.DBStore.Cleanup 删除。name 最长 lock.MaxNameLength 字节。
-- utf8mb4_bin 让锁名区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE locks (
    name VARCHAR(255) NOT NULL PRIMARY KEY,
    owner VARCHAR(64) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    INDEX locks_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE locks;
