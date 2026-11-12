-- +goose Up
-- search_indexes 是索引的版本：查询使用 version，重建期间同步同时写入 version 和 building，重建完成后切换。
-- operation_id 是执行重建的操作（UUID 文本），后发起的重建取代先发起的；last_key 是重建处理到的最后一个 key。
CREATE TABLE search_indexes (
    name TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    building INTEGER,
    operation_id TEXT,
    last_key TEXT
);

-- +goose Down
DROP TABLE search_indexes;
