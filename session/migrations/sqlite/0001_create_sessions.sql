-- +goose Up
-- scs 会话；subject_type、subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除。
-- id 是登录时生成的会话 ID，即主体的 Credential；impersonator_type、impersonator_id 是代为登录时实际操作的人；ip、user_agent、logged_in_at 是登录设备和时间，未登录时为空。
-- expiry、revoked_at 和 logged_in_at 是 Bun 写入的 UTC 文本。
CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    subject_type TEXT,
    subject_id TEXT,
    id TEXT,
    impersonator_type TEXT NOT NULL,
    impersonator_id TEXT NOT NULL,
    ip TEXT NOT NULL,
    user_agent TEXT NOT NULL,
    logged_in_at TEXT,
    data BLOB NOT NULL,
    expiry TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX sessions_expiry ON sessions (expiry);
CREATE INDEX sessions_subject ON sessions (subject_type, subject_id) WHERE subject_id IS NOT NULL;

-- +goose Down
DROP TABLE sessions;
