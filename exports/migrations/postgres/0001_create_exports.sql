-- +goose Up
CREATE TABLE exports (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    owner_type text NOT NULL,
    owner_id text NOT NULL,
    kind text NOT NULL,
    format text NOT NULL,
    status text NOT NULL,
    total_rows integer NOT NULL,
    processed_rows integer NOT NULL,
    file_id uuid,
    filename text NOT NULL,
    url text NOT NULL,
    expires_at timestamptz,
    error text NOT NULL,
    error_data jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz
);
CREATE INDEX exports_owner ON exports (tenant, owner_type, owner_id, id);
CREATE INDEX exports_created_at ON exports (created_at);
COMMENT ON TABLE exports IS '后台导出：导出的进度和生成的文件';
COMMENT ON COLUMN exports.tenant IS '空字符串为平台本身';
COMMENT ON COLUMN exports.status IS 'pending、running、completed 或 failed';
COMMENT ON COLUMN exports.total_rows IS '预计的行数，0 表示不知道';
COMMENT ON COLUMN exports.url IS 'completed 时文件的公开地址，文件在 expires_at 之后被回收';
COMMENT ON COLUMN exports.error IS 'failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据；其他状态为空字符串';

-- +goose Down
DROP TABLE exports;
