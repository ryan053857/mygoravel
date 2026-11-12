-- +goose Up
CREATE TABLE search_indexes (
    name text PRIMARY KEY,
    version integer NOT NULL,
    building integer,
    operation_id uuid,
    last_key text
);
COMMENT ON TABLE search_indexes IS '索引的版本：查询使用 version，重建期间同步同时写入 version 和 building，重建完成后切换';
COMMENT ON COLUMN search_indexes.operation_id IS '执行重建的操作，后发起的重建取代先发起的';
COMMENT ON COLUMN search_indexes.last_key IS '重建处理到的最后一个 key，任务重试时从它之后继续';

-- +goose Down
DROP TABLE search_indexes;
