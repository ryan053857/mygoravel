-- +goose Up
-- webhook 投递记录：一个事件发往一个端点。
-- event_id 即请求头 webhook-id，同一个事件的所有投递相同。payload 是请求体，每次重试原样发送。
-- status 是 pending、succeeded 或 failed；response_status 是最后一次响应的 HTTP 状态码，0 表示没有收到响应。
CREATE TABLE webhook_deliveries (
    id CHAR(36) NOT NULL PRIMARY KEY,
    tenant VARCHAR(191) NOT NULL,
    endpoint_id CHAR(36) NOT NULL,
    event_id CHAR(36) NOT NULL,
    event VARCHAR(191) NOT NULL,
    payload MEDIUMTEXT NOT NULL,
    status VARCHAR(16) NOT NULL,
    attempts INT NOT NULL,
    response_status INT NOT NULL,
    last_error TEXT NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    INDEX webhook_deliveries_tenant (tenant, id),
    INDEX webhook_deliveries_endpoint (tenant, endpoint_id, id),
    INDEX webhook_deliveries_event (event_id),
    INDEX webhook_deliveries_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE webhook_deliveries;
