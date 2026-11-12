-- +goose Up
-- 文档的过滤字段的值，用于过滤、排序和分面；value 编码后按字节比较的顺序就是值的大小顺序。
CREATE TABLE search_values (
    entry_id BIGINT NOT NULL,
    field VARCHAR(64) NOT NULL,
    value VARCHAR(191) NOT NULL,
    PRIMARY KEY (entry_id, field, value),
    INDEX search_values_value (field, value, entry_id),
    CONSTRAINT search_values_entry FOREIGN KEY (entry_id) REFERENCES search_entries (id) ON DELETE CASCADE
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE search_values;
