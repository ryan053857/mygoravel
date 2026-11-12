-- +goose Up
CREATE TABLE exports (
    operation_id uuid PRIMARY KEY REFERENCES operations (id) ON DELETE CASCADE,
    format text NOT NULL,
    file_id uuid,
    filename text NOT NULL,
    url text NOT NULL,
    expires_at timestamptz
);
COMMENT ON TABLE exports IS '后台导出生成的文件，状态和进度在 operations 表中';
COMMENT ON COLUMN exports.url IS 'completed 时文件的公开地址，文件在 expires_at 之后被回收';

-- +goose Down
DROP TABLE exports;
