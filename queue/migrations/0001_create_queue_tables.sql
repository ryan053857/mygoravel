-- +goose Up
CREATE TABLE queue_scheduled (
    id uuid PRIMARY KEY,
    topic text NOT NULL,
    body bytea NOT NULL,
    available_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX queue_scheduled_available_at ON queue_scheduled (available_at);
COMMENT ON TABLE queue_scheduled IS '执行时间离现在超过 max_defer 的任务；快到时间时由 queue.Worker 写回 WAL 后删除';
COMMENT ON COLUMN queue_scheduled.id IS '任务 ID';
COMMENT ON COLUMN queue_scheduled.body IS '发给 NSQ 的消息：任务 ID、payload、元数据和已经执行的次数（JSON）';

CREATE TABLE queue_failed_jobs (
    id uuid PRIMARY KEY,
    topic text NOT NULL,
    payload bytea NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    attempts integer NOT NULL,
    error text NOT NULL,
    failed_at timestamptz NOT NULL
);
CREATE INDEX queue_failed_jobs_failed_at ON queue_failed_jobs (failed_at);
COMMENT ON TABLE queue_failed_jobs IS '重试次数用完或永久失败的任务；由 queue.FailedJobs 重新发布或清理';

-- +goose Down
DROP TABLE queue_failed_jobs;
DROP TABLE queue_scheduled;
