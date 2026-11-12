-- +goose Up
-- monitor.Recorder 按分钟聚合的记录；过期的分钟由 monitor.Store.Cleanup 删除。bucket 是这一分钟的开始，Unix 秒。
CREATE TABLE monitor_aggregates (
    metric TEXT NOT NULL,
    label TEXT NOT NULL,
    bucket INTEGER NOT NULL,
    count INTEGER NOT NULL,
    sum REAL NOT NULL,
    max REAL NOT NULL,
    PRIMARY KEY (metric, label, bucket)
);
CREATE INDEX monitor_aggregates_bucket ON monitor_aggregates (bucket);

-- monitor.Recorder.Set 记录的最新值，每个 metric 和 label 一行。value 是 JSON，recorded_at 是 Unix 微秒。
CREATE TABLE monitor_values (
    metric TEXT NOT NULL,
    label TEXT NOT NULL,
    value TEXT NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY (metric, label)
);

-- +goose Down
DROP TABLE monitor_values;
DROP TABLE monitor_aggregates;
