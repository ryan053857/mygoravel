-- +goose Up
-- UNLOGGED 不写 WAL：崩溃后表被清空，相当于所有 key 的配额回满，对限流可以接受。
CREATE UNLOGGED TABLE rate_limits (
    name text NOT NULL,
    key text NOT NULL,
    tat bigint NOT NULL,
    PRIMARY KEY (name, key)
);
CREATE INDEX rate_limits_tat ON rate_limits (tat);
COMMENT ON TABLE rate_limits IS 'ratelimit.Database 的理论到达时间；tat 已过去的行由 Cleanup 删除';
COMMENT ON COLUMN rate_limits.tat IS '理论到达时间，Unix 微秒，取自应用时钟';

-- +goose Down
DROP TABLE rate_limits;
