-- +goose Up
CREATE TABLE sessions (
    token text PRIMARY KEY,
    subject_id text,
    id text,
    impersonator text NOT NULL,
    ip text NOT NULL,
    user_agent text NOT NULL,
    logged_in_at timestamptz,
    data bytea NOT NULL,
    expiry timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX sessions_expiry ON sessions (expiry);
CREATE INDEX sessions_subject_id ON sessions (subject_id) WHERE subject_id IS NOT NULL;
COMMENT ON TABLE sessions IS 'scs 会话；subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除';
COMMENT ON COLUMN sessions.id IS '会话 ID，登录时生成，即主体的 Credential；未登录时为空';
COMMENT ON COLUMN sessions.impersonator IS '代为登录时实际操作的人；空字符串表示用户本人';
COMMENT ON COLUMN sessions.ip IS '登录请求的客户端地址；ip、user_agent、logged_in_at 在未登录时为空';

-- +goose Down
DROP TABLE sessions;
