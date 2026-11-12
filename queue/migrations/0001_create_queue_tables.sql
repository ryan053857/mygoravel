-- +goose Up
CREATE TABLE queue_outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic text NOT NULL,
    body bytea NOT NULL,
    available_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX queue_outbox_available_at ON queue_outbox (available_at);
COMMENT ON TABLE queue_outbox IS '已发布、还没转发到 NSQ 的任务；与业务数据在同一个事务中写入，由 queue.Worker 转发后删除';
COMMENT ON COLUMN queue_outbox.body IS '发给 NSQ 的消息：任务 ID、payload、元数据和已经执行的次数（JSON）';
COMMENT ON COLUMN queue_outbox.available_at IS '可以执行的时间；离现在不超过 max_defer 时转发为 NSQ 的延迟消息';

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
DROP TABLE queue_outbox;
