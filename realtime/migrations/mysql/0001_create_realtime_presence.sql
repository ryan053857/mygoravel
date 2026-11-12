-- +goose Up
-- 实时频道的在线成员，每个连接在每个频道中一行；连接定期续期，过期行由 realtime.DatabasePresence.Cleanup 删除。
-- client_id 是连接的 ID，同一个主体的多个连接各有一行；info 是成员资料的 JSON，没有资料时为空。
CREATE TABLE realtime_presence (
    channel VARCHAR(200) NOT NULL,
    client_id VARCHAR(64) NOT NULL,
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    info BLOB NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    PRIMARY KEY (channel, client_id),
    INDEX realtime_presence_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE realtime_presence;
