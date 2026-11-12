-- +goose Up
CREATE TABLE push_devices (
    id uuid PRIMARY KEY,
    platform text NOT NULL CHECK (platform IN ('android', 'ios', 'harmony')),
    token text NOT NULL,
    tenant text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    credential text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT push_devices_token_unique UNIQUE (platform, token)
);
CREATE INDEX push_devices_subject ON push_devices (subject_type, subject_id);
CREATE INDEX push_devices_updated_at ON push_devices (updated_at);
COMMENT ON TABLE push_devices IS '接收移动推送的设备：一台设备同时只属于一个主体，由 push.Register 写入';
COMMENT ON COLUMN push_devices.token IS '推送服务给设备的标识，例如阿里云移动推送的 deviceId';
COMMENT ON COLUMN push_devices.tenant IS '注册时所在的租户，空字符串为平台本身';
COMMENT ON COLUMN push_devices.credential IS '注册时所用凭据的 ID（identity.Subject.Credential），撤销这个凭据时一起删除；可为空字符串';
COMMENT ON COLUMN push_devices.updated_at IS '最近一次注册的时间，长期没有更新的设备由 push.Cleanup 删除';

-- +goose Down
DROP TABLE push_devices;
