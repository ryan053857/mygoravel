-- +goose Up
-- 后台导入中没有导入的行。cells 是这一行的原始单元格（JSON 文本），与 imports.header 对应；errors 是以字段名为键的错误提示。
CREATE TABLE import_failures (
    import_id TEXT NOT NULL,
    line INTEGER NOT NULL,
    cells TEXT NOT NULL,
    errors TEXT NOT NULL,
    message TEXT NOT NULL,
    PRIMARY KEY (import_id, line)
);

-- +goose Down
DROP TABLE import_failures;
