-- +goose Up
CREATE TABLE import_failures (
    import_id uuid NOT NULL,
    line integer NOT NULL,
    cells jsonb NOT NULL,
    errors jsonb NOT NULL,
    message text NOT NULL,
    PRIMARY KEY (import_id, line)
);
COMMENT ON TABLE import_failures IS '后台导入中没有导入的行';
COMMENT ON COLUMN import_failures.cells IS '这一行的原始单元格，与 imports.header 对应';
COMMENT ON COLUMN import_failures.errors IS '以字段名为键的错误提示';

-- +goose Down
DROP TABLE import_failures;
