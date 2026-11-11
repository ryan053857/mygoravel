-- +goose Up
-- 待处理和处理中的队列任务，处理成功后删除。id 是 UUID 文本，时间是 Bun 写入的 UTC 文本，metadata 是 JSON 文本。
-- available_at：可执行时间；被领取后是租约到期时间，到期未完成的任务可被重新领取。
-- reserved_at：最近一次被领取的时间；等待执行或重试时为空。
-- unique_key：发布时指定的合并键；同一 topic 中还没被领取过的任务不重复，第一次被领取时清空。
CREATE TABLE queue_jobs (
    id TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    payload BLOB NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}',
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TEXT NOT NULL,
    reserved_at TEXT,
    unique_key TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX queue_jobs_topic_available_at ON queue_jobs (topic, available_at);
CREATE UNIQUE INDEX queue_jobs_topic_unique_key ON queue_jobs (topic, unique_key);

-- 重试次数用完或永久失败的任务，与队列驱动无关；由 queue.FailedJobs 重新入队或清理。
-- driver：任务所在的队列驱动，重新入队时放回原驱动。
CREATE TABLE queue_failed_jobs (
    id TEXT PRIMARY KEY,
    driver TEXT NOT NULL,
    topic TEXT NOT NULL,
    payload BLOB NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}',
    attempts INTEGER NOT NULL,
    error TEXT NOT NULL,
    failed_at TEXT NOT NULL
);
CREATE INDEX queue_failed_jobs_failed_at ON queue_failed_jobs (failed_at);

-- +goose Down
DROP TABLE queue_failed_jobs;
DROP TABLE queue_jobs;
