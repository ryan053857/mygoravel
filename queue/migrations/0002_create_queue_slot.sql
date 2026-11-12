-- 任务写入 WAL（pg_logical_emit_message），Worker 经由这个逻辑复制槽读取后转发到 NSQ。
-- 槽必须在第一次发布任务之前存在：建立之前写入的任务不会被读到。需要 PostgreSQL 17 以上（failover 槽）、
-- wal_level=logical，执行迁移的用户需要 REPLICATION 属性；条件不满足时这个迁移失败。
-- 创建逻辑复制槽不能在已经写过数据的事务中进行，所以不使用事务。

-- +goose NO TRANSACTION
-- +goose Up
CREATE PUBLICATION mygoravel_queue;
SELECT pg_create_logical_replication_slot('mygoravel_queue_' || left(md5(current_database()), 16), 'pgoutput', false, false, true);

-- +goose Down
SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots
WHERE slot_name = 'mygoravel_queue_' || left(md5(current_database()), 16);
DROP PUBLICATION IF EXISTS mygoravel_queue;
