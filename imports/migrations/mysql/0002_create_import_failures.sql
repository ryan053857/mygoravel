-- +goose Up
-- 后台导入中没有导入的行。cells 是这一行的原始单元格，与 imports.header 对应；errors 是以字段名为键的错误提示。
CREATE TABLE import_failures (
    import_id CHAR(36) NOT NULL,
    line INT NOT NULL,
    cells JSON NOT NULL,
    errors JSON NOT NULL,
    message TEXT NOT NULL,
    PRIMARY KEY (import_id, line)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE import_failures;
