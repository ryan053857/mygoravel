-- +goose Up
CREATE TABLE monitor_aggregates (
    metric text NOT NULL,
    label text NOT NULL,
    bucket bigint NOT NULL,
    count bigint NOT NULL,
    sum double precision NOT NULL,
    max double precision NOT NULL,
    PRIMARY KEY (metric, label, bucket)
);
CREATE INDEX monitor_aggregates_bucket ON monitor_aggregates (bucket);
COMMENT ON TABLE monitor_aggregates IS 'monitor.Recorder 按分钟聚合的记录；过期的分钟由 monitor.Store.Cleanup 删除';
COMMENT ON COLUMN monitor_aggregates.bucket IS '这一分钟的开始，Unix 秒';

CREATE TABLE monitor_values (
    metric text NOT NULL,
    label text NOT NULL,
    value text NOT NULL,
    recorded_at bigint NOT NULL,
    PRIMARY KEY (metric, label)
);
COMMENT ON TABLE monitor_values IS 'monitor.Recorder.Set 记录的最新值，每个 metric 和 label 一行';
COMMENT ON COLUMN monitor_values.value IS 'JSON';
COMMENT ON COLUMN monitor_values.recorded_at IS '记录的时间，Unix 微秒';

-- +goose Down
DROP TABLE monitor_values;
DROP TABLE monitor_aggregates;
