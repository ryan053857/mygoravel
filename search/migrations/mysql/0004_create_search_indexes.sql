-- +goose Up
-- 索引的版本：查询使用 version，重建期间同步同时写入 version 和 building，重建完成后切换。
-- operation_id 是执行重建的操作，后发起的重建取代先发起的；last_key 是重建处理到的最后一个 key。
CREATE TABLE search_indexes (
    name VARCHAR(64) NOT NULL PRIMARY KEY,
    version INT NOT NULL,
    building INT NULL,
    operation_id CHAR(36) NULL,
    last_key VARCHAR(191) NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE search_indexes;
