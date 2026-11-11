-- +goose Up
-- 站内通知：发给某个租户中某个主体（recipient）的通知。
-- tenant 为空字符串时是平台本身；read_at 为 NULL 表示未读。
-- id 是 UUID 文本，data 是 JSON 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE notifications (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    recipient_type TEXT NOT NULL,
    recipient_id TEXT NOT NULL,
    event TEXT NOT NULL,
    data TEXT NOT NULL,
    read_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX notifications_recipient ON notifications (tenant, recipient_type, recipient_id, id);
CREATE INDEX notifications_unread ON notifications (tenant, recipient_type, recipient_id, id) WHERE read_at IS NULL;
CREATE INDEX notifications_created_at ON notifications (created_at);

-- +goose Down
DROP TABLE notifications;
