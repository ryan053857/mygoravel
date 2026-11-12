-- +goose Up
-- search_values 是文档的过滤字段的值，用于过滤、排序和分面；value 编码后按字节比较的顺序就是值的大小顺序。
CREATE TABLE search_values (
    entry_id INTEGER NOT NULL REFERENCES search_entries (id) ON DELETE CASCADE,
    field TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (entry_id, field, value)
) WITHOUT ROWID;
CREATE INDEX search_values_value ON search_values (field, value, entry_id);

-- +goose Down
DROP TABLE search_values;
