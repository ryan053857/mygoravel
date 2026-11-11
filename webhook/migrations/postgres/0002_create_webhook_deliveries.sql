-- +goose Up
CREATE TABLE webhook_deliveries (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    endpoint_id uuid NOT NULL,
    event_id uuid NOT NULL,
    event text NOT NULL,
    payload text NOT NULL,
    status text NOT NULL,
    attempts integer NOT NULL,
    response_status integer NOT NULL,
    last_error text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX webhook_deliveries_tenant ON webhook_deliveries (tenant, id);
CREATE INDEX webhook_deliveries_endpoint ON webhook_deliveries (tenant, endpoint_id, id);
CREATE INDEX webhook_deliveries_event ON webhook_deliveries (event_id);
CREATE INDEX webhook_deliveries_created_at ON webhook_deliveries (created_at);
COMMENT ON TABLE webhook_deliveries IS 'webhook 投递记录：一个事件发往一个端点';
COMMENT ON COLUMN webhook_deliveries.event_id IS '请求头 webhook-id，同一个事件的所有投递相同';
COMMENT ON COLUMN webhook_deliveries.payload IS '请求体，每次重试原样发送；用 text 保存，保持签名时的原始字节';
COMMENT ON COLUMN webhook_deliveries.status IS 'pending、succeeded 或 failed';
COMMENT ON COLUMN webhook_deliveries.response_status IS '最后一次响应的 HTTP 状态码，0 表示没有收到响应';

-- +goose Down
DROP TABLE webhook_deliveries;
