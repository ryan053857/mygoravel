-- +goose Up
-- monitor.Recorder 按分钟聚合的记录；过期的分钟由 monitor.Store.Cleanup 删除。bucket 是这一分钟的开始，Unix 秒。
CREATE TABLE monitor_aggregates (
    metric VARCHAR(100) NOT NULL,
    label VARCHAR(255) NOT NULL,
    bucket BIGINT NOT NULL,
    count BIGINT NOT NULL,
    sum DOUBLE NOT NULL,
    max DOUBLE NOT NULL,
    PRIMARY KEY (metric, label, bucket),
    INDEX monitor_aggregates_bucket (bucket)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- monitor.Recorder.Set 记录的最新值，每个 metric 和 label 一行。value 是 JSON，recorded_at 是 Unix 微秒。
CREATE TABLE monitor_values (
    metric VARCHAR(100) NOT NULL,
    label VARCHAR(255) NOT NULL,
    value MEDIUMTEXT NOT NULL,
    recorded_at BIGINT NOT NULL,
    PRIMARY KEY (metric, label)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE monitor_values;
DROP TABLE monitor_aggregates;
