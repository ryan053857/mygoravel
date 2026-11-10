-- +goose Up
CREATE TABLE sessions (
    token text PRIMARY KEY,
    data bytea NOT NULL,
    expiry timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions (expiry);
COMMENT ON TABLE sessions IS 'scs 会话；过期行由 session.Store.Cleanup 删除';

-- +goose Down
DROP TABLE sessions;
