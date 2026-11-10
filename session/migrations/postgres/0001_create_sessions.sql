-- +goose Up
CREATE TABLE sessions (
    token text PRIMARY KEY,
    subject_id text,
    data bytea NOT NULL,
    expiry timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX sessions_expiry ON sessions (expiry);
CREATE INDEX sessions_subject_id ON sessions (subject_id) WHERE subject_id IS NOT NULL;
COMMENT ON TABLE sessions IS 'scs 会话；subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除';

-- +goose Down
DROP TABLE sessions;
