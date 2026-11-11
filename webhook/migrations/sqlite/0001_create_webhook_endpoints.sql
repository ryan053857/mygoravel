-- +goose Up
-- webhook 端点：租户配置的接收地址。tenant 为空字符串时是平台本身。
-- events 是订阅的事件类型（JSON 文本）：完整类型、以 .* 结尾的前缀或 *。
-- previous_secret 是轮换前的密钥，在 previous_secret_expires_at 之前仍参与签名；没有时为空字符串。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本，disabled 为 0 或 1。
CREATE TABLE webhook_endpoints (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    url TEXT NOT NULL,
    description TEXT NOT NULL,
    events TEXT NOT NULL,
    secret TEXT NOT NULL,
    disabled INTEGER NOT NULL,
    previous_secret TEXT NOT NULL,
    previous_secret_expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX webhook_endpoints_tenant ON webhook_endpoints (tenant, id);

-- +goose Down
DROP TABLE webhook_endpoints;
