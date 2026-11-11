-- +goose Up
-- 待处理和处理中的队列任务，处理成功后删除。id 是 UUID 文本，metadata 是 JSON。
-- available_at：可执行时间；被领取后是租约到期时间，到期未完成的任务可被重新领取。
-- reserved_at：最近一次被领取的时间；等待执行或重试时为空。
-- unique_key：发布时指定的合并键；同一 topic 中还没被领取过的任务不重复，第一次被领取时清空。
-- MySQL 的 DDL 会隐式提交，两张表用 IF NOT EXISTS 创建：第二条失败后修复并重新执行，第一条不会报错。
-- utf8mb4_bin 让 topic 区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE IF NOT EXISTS queue_jobs (
    id CHAR(36) NOT NULL PRIMARY KEY,
    topic VARCHAR(255) NOT NULL,
    payload LONGBLOB NOT NULL,
    metadata JSON NOT NULL DEFAULT (JSON_OBJECT()),
    attempts INT NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL,
    reserved_at DATETIME(6),
    unique_key VARCHAR(255),
    created_at DATETIME(6) NOT NULL,
    INDEX queue_jobs_topic_available_at (topic, available_at),
    UNIQUE INDEX queue_jobs_topic_unique_key (topic, unique_key)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- 重试次数用完或永久失败的任务，与队列驱动无关；由 queue.FailedJobs 重新入队或清理。
-- driver：任务所在的队列驱动，重新入队时放回原驱动。
CREATE TABLE IF NOT EXISTS queue_failed_jobs (
    id CHAR(36) NOT NULL PRIMARY KEY,
    driver VARCHAR(32) NOT NULL,
    topic VARCHAR(255) NOT NULL,
    payload LONGBLOB NOT NULL,
    metadata JSON NOT NULL DEFAULT (JSON_OBJECT()),
    attempts INT NOT NULL,
    error MEDIUMTEXT NOT NULL,
    failed_at DATETIME(6) NOT NULL,
    INDEX queue_failed_jobs_failed_at (failed_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE queue_failed_jobs;
DROP TABLE queue_jobs;
