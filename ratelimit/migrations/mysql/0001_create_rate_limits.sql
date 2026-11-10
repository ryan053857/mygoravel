-- +goose Up
-- ratelimit.Database 的理论到达时间；tat 已过去的行由 Cleanup 删除。
-- tat 是 Unix 微秒，取自应用时钟。超过 255 字节的 key 以摘要保存。
CREATE TABLE rate_limits (
    name VARCHAR(255) NOT NULL,
    `key` VARCHAR(255) NOT NULL,
    tat BIGINT NOT NULL,
    PRIMARY KEY (name, `key`),
    INDEX rate_limits_tat (tat)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE rate_limits;
