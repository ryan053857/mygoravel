-- +goose Up
-- 后台导入：一个上传的表格文件和它的处理进度。tenant 为空字符串时是平台本身。
-- status 是 pending、running、completed 或 failed；last_line 是已经处理到的行号，任务重试时从下一行继续。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据（JSON 文本）；其他状态为空字符串。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE imports (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    file_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    locale TEXT NOT NULL,
    status TEXT NOT NULL,
    header TEXT NOT NULL,
    total_rows INTEGER NOT NULL,
    processed_rows INTEGER NOT NULL,
    failed_rows INTEGER NOT NULL,
    last_line INTEGER NOT NULL,
    error TEXT NOT NULL,
    error_data TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX imports_owner ON imports (tenant, owner_type, owner_id, id);
CREATE INDEX imports_created_at ON imports (created_at);

-- +goose Down
DROP TABLE imports;
