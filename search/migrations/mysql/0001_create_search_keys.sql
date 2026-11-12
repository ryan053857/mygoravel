-- +goose Up
-- 已同步到搜索索引的 key，同步时锁住它，使同一个 key 的同步依次执行。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE search_keys (
    index_name VARCHAR(64) NOT NULL,
    doc_key VARCHAR(191) NOT NULL,
    synced_at DATETIME(6) NOT NULL,
    PRIMARY KEY (index_name, doc_key)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE search_keys;
