-- +goose Up
-- 操作日志：谁（actor）在哪个租户中对什么（target）做了什么（event）。
-- tenant 为空字符串时是平台本身；actor_id 为空字符串表示没有操作人。
-- id 是 UUID 文本，properties 是 JSON 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE activity_log (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    event TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    properties TEXT NOT NULL,
    request_id TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX activity_log_tenant ON activity_log (tenant, id);
CREATE INDEX activity_log_target ON activity_log (tenant, target_type, target_id, id);
CREATE INDEX activity_log_actor ON activity_log (tenant, actor_id, id);
CREATE INDEX activity_log_created_at ON activity_log (created_at);

-- +goose Down
DROP TABLE activity_log;
