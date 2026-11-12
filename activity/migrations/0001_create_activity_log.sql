-- +goose Up
CREATE TABLE activity_log (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    actor_type text NOT NULL,
    actor_id text NOT NULL,
    impersonator_type text NOT NULL,
    impersonator_id text NOT NULL,
    event text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    properties jsonb NOT NULL,
    request_id text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX activity_log_tenant ON activity_log (tenant, id);
CREATE INDEX activity_log_target ON activity_log (tenant, target_type, target_id, id);
CREATE INDEX activity_log_actor ON activity_log (tenant, actor_type, actor_id, id);
CREATE INDEX activity_log_created_at ON activity_log (created_at);
COMMENT ON TABLE activity_log IS '操作日志：谁（actor）在哪个租户中对什么（target）做了什么（event）';
COMMENT ON COLUMN activity_log.tenant IS '空字符串为平台本身';
COMMENT ON COLUMN activity_log.actor_id IS '空字符串表示没有操作人';
COMMENT ON COLUMN activity_log.impersonator_type IS '代为登录时实际操作的人，与 impersonator_id 一起引用；空字符串表示操作人本人';

-- +goose Down
DROP TABLE activity_log;
