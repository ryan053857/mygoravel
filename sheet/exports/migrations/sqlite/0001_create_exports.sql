-- +goose Up
-- 后台导出生成的文件，状态和进度在 operations 表中，删除操作时级联删除。
-- url 是 completed 时文件的公开地址，文件在 expires_at 之后被回收。id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE exports (
    operation_id TEXT PRIMARY KEY REFERENCES operations (id) ON DELETE CASCADE,
    format TEXT NOT NULL,
    file_id TEXT,
    filename TEXT NOT NULL,
    url TEXT NOT NULL,
    expires_at TEXT
);

-- +goose Down
DROP TABLE exports;
