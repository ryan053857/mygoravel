-- +goose Up
-- 广播消息；各实例按 id 轮询，旧行由 broadcast.Database.Cleanup 删除。
CREATE TABLE broadcasts (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    topic VARCHAR(200) NOT NULL,
    data MEDIUMBLOB NOT NULL,
    created_at DATETIME(6) NOT NULL,
    INDEX broadcasts_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE broadcasts;
