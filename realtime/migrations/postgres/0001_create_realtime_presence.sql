-- +goose Up
CREATE TABLE realtime_presence (
    channel text NOT NULL,
    client_id text NOT NULL,
    subject_id text NOT NULL,
    info bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (channel, client_id)
);
CREATE INDEX realtime_presence_expires_at ON realtime_presence (expires_at);
COMMENT ON TABLE realtime_presence IS '实时频道的在线成员，每个连接在每个频道中一行；连接定期续期，过期行由 realtime.DatabasePresence.Cleanup 删除';
COMMENT ON COLUMN realtime_presence.client_id IS '连接的 ID；同一个主体的多个连接各有一行';
COMMENT ON COLUMN realtime_presence.info IS '成员资料的 JSON，订阅时由频道规则给出；没有资料时为空';

-- +goose Down
DROP TABLE realtime_presence;
