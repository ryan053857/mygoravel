-- +goose Up
-- 实时频道的在线成员，每个连接在每个频道中一行；连接定期续期，过期行由 realtime.DatabasePresence.Cleanup 删除。
-- client_id 是连接的 ID，同一个主体的多个连接各有一行；info 是成员资料的 JSON，没有资料时为空。
-- expires_at 是 Bun 写入的 UTC 文本。
CREATE TABLE realtime_presence (
    channel TEXT NOT NULL,
    client_id TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    info BLOB NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (channel, client_id)
);
CREATE INDEX realtime_presence_expires_at ON realtime_presence (expires_at);

-- +goose Down
DROP TABLE realtime_presence;
