-- +goose Up
CREATE TABLE queue_jobs (
    id uuid PRIMARY KEY,
    topic text NOT NULL,
    payload bytea NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL,
    reserved_at timestamptz,
    created_at timestamptz NOT NULL
);
CREATE INDEX queue_jobs_topic_available_at ON queue_jobs (topic, available_at);
COMMENT ON TABLE queue_jobs IS '待处理和处理中的队列任务；处理成功后删除';
COMMENT ON COLUMN queue_jobs.available_at IS '可执行时间；被领取后是租约到期时间，到期未完成的任务可被重新领取';
COMMENT ON COLUMN queue_jobs.reserved_at IS '最近一次被领取的时间；等待执行或重试时为空';

CREATE TABLE queue_failed_jobs (
    id uuid PRIMARY KEY,
    driver text NOT NULL,
    topic text NOT NULL,
    payload bytea NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    attempts integer NOT NULL,
    error text NOT NULL,
    failed_at timestamptz NOT NULL
);
CREATE INDEX queue_failed_jobs_failed_at ON queue_failed_jobs (failed_at);
COMMENT ON TABLE queue_failed_jobs IS '重试次数用完或永久失败的任务，与队列驱动无关；由 queue.FailedJobs 重新入队或清理';
COMMENT ON COLUMN queue_failed_jobs.driver IS '任务所在的队列驱动，重新入队时放回原驱动';

-- +goose Down
DROP TABLE queue_failed_jobs;
DROP TABLE queue_jobs;
