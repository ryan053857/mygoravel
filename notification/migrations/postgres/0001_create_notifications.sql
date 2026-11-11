-- +goose Up
CREATE TABLE notifications (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    recipient_type text NOT NULL,
    recipient_id text NOT NULL,
    event text NOT NULL,
    data jsonb NOT NULL,
    read_at timestamptz,
    created_at timestamptz NOT NULL
);
CREATE INDEX notifications_recipient ON notifications (tenant, recipient_type, recipient_id, id);
CREATE INDEX notifications_unread ON notifications (tenant, recipient_type, recipient_id, id) WHERE read_at IS NULL;
CREATE INDEX notifications_created_at ON notifications (created_at);
COMMENT ON TABLE notifications IS '站内通知：发给某个租户中某个主体（recipient）的通知';
COMMENT ON COLUMN notifications.tenant IS '空字符串为平台本身';
COMMENT ON COLUMN notifications.read_at IS 'NULL 表示未读';

-- +goose Down
DROP TABLE notifications;
