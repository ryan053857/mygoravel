-- +goose Up
CREATE TABLE vector_indexes (
    name text PRIMARY KEY,
    version integer NOT NULL,
    building integer,
    operation_id uuid,
    last_key text
);
COMMENT ON TABLE vector_indexes IS '向量索引的版本，每个版本是一个 Qdrant collection：查询使用 version，重建期间同步同时写入 version 和 building，重建完成后切换';
COMMENT ON COLUMN vector_indexes.operation_id IS '执行重建的操作，后发起的重建取代先发起的';
COMMENT ON COLUMN vector_indexes.last_key IS '重建处理到的最后一个 key，任务重试时从它之后继续';

-- +goose Down
DROP TABLE vector_indexes;
