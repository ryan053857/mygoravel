-- +goose Up
-- scs 会话；过期行由 session.Store.Cleanup 删除。expiry 是 Bun 写入的 UTC 文本。
CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    data BLOB NOT NULL,
    expiry TEXT NOT NULL
);
CREATE INDEX sessions_expiry ON sessions (expiry);

-- +goose Down
DROP TABLE sessions;
