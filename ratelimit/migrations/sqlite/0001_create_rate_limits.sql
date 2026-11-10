-- +goose Up
-- ratelimit.Database 的理论到达时间；tat 已过去的行由 Cleanup 删除。
-- tat 是 Unix 微秒整数，由应用时钟计算。
CREATE TABLE rate_limits (
    name TEXT NOT NULL,
    key TEXT NOT NULL,
    tat INTEGER NOT NULL,
    PRIMARY KEY (name, key)
);
CREATE INDEX rate_limits_tat ON rate_limits (tat);

-- +goose Down
DROP TABLE rate_limits;
