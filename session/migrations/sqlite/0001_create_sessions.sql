-- +goose Up
-- scs 会话；subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除。
-- expiry 和 revoked_at 是 Bun 写入的 UTC 文本。
CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    subject_id TEXT,
    data BLOB NOT NULL,
    expiry TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX sessions_expiry ON sessions (expiry);
CREATE INDEX sessions_subject_id ON sessions (subject_id) WHERE subject_id IS NOT NULL;

-- +goose Down
DROP TABLE sessions;
