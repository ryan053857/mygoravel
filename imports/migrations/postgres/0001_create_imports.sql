-- +goose Up
CREATE TABLE imports (
    id uuid PRIMARY KEY,
    tenant text NOT NULL,
    owner_type text NOT NULL,
    owner_id text NOT NULL,
    kind text NOT NULL,
    file_id uuid NOT NULL,
    filename text NOT NULL,
    locale text NOT NULL,
    status text NOT NULL,
    header jsonb NOT NULL,
    total_rows integer NOT NULL,
    processed_rows integer NOT NULL,
    failed_rows integer NOT NULL,
    last_line integer NOT NULL,
    error text NOT NULL,
    error_data jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz
);
CREATE INDEX imports_owner ON imports (tenant, owner_type, owner_id, id);
CREATE INDEX imports_created_at ON imports (created_at);
COMMENT ON TABLE imports IS '后台导入：一个上传的表格文件和它的处理进度';
COMMENT ON COLUMN imports.tenant IS '空字符串为平台本身';
COMMENT ON COLUMN imports.status IS 'pending、running、completed 或 failed';
COMMENT ON COLUMN imports.last_line IS '已经处理到的行号，任务重试时从下一行继续';
COMMENT ON COLUMN imports.error IS 'failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据；其他状态为空字符串';

-- +goose Down
DROP TABLE imports;
