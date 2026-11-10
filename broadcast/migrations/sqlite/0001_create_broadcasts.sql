-- +goose Up
-- 广播消息；各进程按 id 轮询，旧行由 broadcast.Database.Cleanup 删除。
-- AUTOINCREMENT 保证 id 不会在删除最大的行之后被重新使用。created_at 是 Bun 写入的 UTC 文本。
CREATE TABLE broadcasts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    topic TEXT NOT NULL,
    data BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX broadcasts_created_at ON broadcasts (created_at);

-- +goose Down
DROP TABLE broadcasts;
