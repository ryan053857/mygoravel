-- +goose Up
CREATE TABLE webhook_endpoints (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    url text NOT NULL,
    description text NOT NULL,
    events jsonb NOT NULL,
    secret text NOT NULL,
    disabled boolean NOT NULL,
    previous_secret text NOT NULL,
    previous_secret_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX webhook_endpoints_tenant ON webhook_endpoints (tenant, id);
COMMENT ON TABLE webhook_endpoints IS 'webhook 端点：租户配置的接收地址';
COMMENT ON COLUMN webhook_endpoints.tenant IS '空字符串为平台本身';
COMMENT ON COLUMN webhook_endpoints.events IS '订阅的事件类型：完整类型、以 .* 结尾的前缀或 *';
COMMENT ON COLUMN webhook_endpoints.previous_secret IS '轮换前的密钥，在 previous_secret_expires_at 之前仍参与签名；没有时为空字符串';

-- +goose Down
DROP TABLE webhook_endpoints;
