-- +goose Up
-- 原子锁；过期行由 lock.DBStore.Cleanup 删除。expires_at 是 Bun 写入的 UTC 文本。
CREATE TABLE locks (
    name TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX locks_expires_at ON locks (expires_at);

-- +goose Down
DROP TABLE locks;
