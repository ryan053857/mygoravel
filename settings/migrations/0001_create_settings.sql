-- +goose Up
CREATE TABLE settings (
    name text NOT NULL,
    tenant text NOT NULL,
    payload jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (name, tenant)
);
COMMENT ON TABLE settings IS '运行时可修改的设置：每组设置一行，整组保存为 JSON';
COMMENT ON COLUMN settings.tenant IS '租户的设置为租户 ID，全局设置为空字符串';
COMMENT ON COLUMN settings.payload IS '整组设置的 JSON；缺少的字段读取时使用代码中的默认值';

-- +goose Down
DROP TABLE settings;
