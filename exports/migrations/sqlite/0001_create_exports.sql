-- +goose Up
-- 后台导出：导出的进度和生成的文件。tenant 为空字符串时是平台本身。
-- status 是 pending、running、completed 或 failed；total_rows 是预计的行数，0 表示不知道。
-- url 是 completed 时文件的公开地址，文件在 expires_at 之后被回收。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据（JSON 文本）；其他状态为空字符串。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE exports (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    format TEXT NOT NULL,
    status TEXT NOT NULL,
    total_rows INTEGER NOT NULL,
    processed_rows INTEGER NOT NULL,
    file_id TEXT,
    filename TEXT NOT NULL,
    url TEXT NOT NULL,
    expires_at TEXT,
    error TEXT NOT NULL,
    error_data TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX exports_owner ON exports (tenant, owner_type, owner_id, id);
CREATE INDEX exports_created_at ON exports (created_at);

-- +goose Down
DROP TABLE exports;
