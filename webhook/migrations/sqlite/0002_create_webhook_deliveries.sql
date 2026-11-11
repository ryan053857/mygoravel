-- +goose Up
-- webhook 投递记录：一个事件发往一个端点。
-- event_id 即请求头 webhook-id，同一个事件的所有投递相同。payload 是请求体，每次重试原样发送。
-- status 是 pending、succeeded 或 failed；response_status 是最后一次响应的 HTTP 状态码，0 表示没有收到响应。
CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    endpoint_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    event TEXT NOT NULL,
    payload TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    response_status INTEGER NOT NULL,
    last_error TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX webhook_deliveries_tenant ON webhook_deliveries (tenant, id);
CREATE INDEX webhook_deliveries_endpoint ON webhook_deliveries (tenant, endpoint_id, id);
CREATE INDEX webhook_deliveries_event ON webhook_deliveries (event_id);
CREATE INDEX webhook_deliveries_created_at ON webhook_deliveries (created_at);

-- +goose Down
DROP TABLE webhook_deliveries;
