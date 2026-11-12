-- +goose Up
CREATE TABLE broadcasts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic text NOT NULL,
    data bytea NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX broadcasts_created_at ON broadcasts (created_at);
COMMENT ON TABLE broadcasts IS '广播消息；各实例按 id 轮询，旧行由 broadcast.Database.Cleanup 删除';

-- +goose Down
DROP TABLE broadcasts;
