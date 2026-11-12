-- +goose Up
CREATE TABLE search_values (
    entry_id bigint NOT NULL REFERENCES search_entries (id) ON DELETE CASCADE,
    field text NOT NULL,
    value text COLLATE "C" NOT NULL,
    PRIMARY KEY (entry_id, field, value)
);
CREATE INDEX search_values_value ON search_values (field, value, entry_id);
COMMENT ON TABLE search_values IS '文档的过滤字段的值，用于过滤、排序和分面';
COMMENT ON COLUMN search_values.value IS '编码后的值，按字节比较的顺序就是值的大小顺序';

-- +goose Down
DROP TABLE search_values;
