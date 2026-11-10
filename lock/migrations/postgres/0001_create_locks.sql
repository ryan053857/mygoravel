-- +goose Up
CREATE TABLE locks (
    name text PRIMARY KEY,
    owner text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX locks_expires_at ON locks (expires_at);
COMMENT ON TABLE locks IS '原子锁；过期行由 lock.DBStore.Cleanup 删除';

-- +goose Down
DROP TABLE locks;
