-- +goose Up
-- search_keys 是已同步到搜索索引的 key，同步时锁住它，使同一个 key 的同步依次执行。
CREATE TABLE search_keys (
    index_name TEXT NOT NULL,
    doc_key TEXT NOT NULL,
    synced_at TEXT NOT NULL,
    PRIMARY KEY (index_name, doc_key)
);

-- +goose Down
DROP TABLE search_keys;
