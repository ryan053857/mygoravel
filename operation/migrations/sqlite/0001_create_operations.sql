-- +goose Up
-- 用户在后台发起的长操作，例如导入、导出和批量处理，记录它的状态和进度。tenant 为空字符串时是平台本身。
-- kind 由应用定义，例如 articles.import；status 是 pending、running、completed 或 failed。
-- total 是预计的单位数，0 表示还不知道；processed 是已经处理的单位数，包括失败的单位。
-- version 每次保存加一，同一个操作的两次执行据此发现对方。
-- error 是 failed 时的错误码，按 i18n 消息翻译，error_data 是模板数据（JSON 文本）；其他状态为空字符串。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    tenant TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    total INTEGER NOT NULL,
    processed INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    version INTEGER NOT NULL,
    error TEXT NOT NULL,
    error_data TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX operations_owner ON operations (tenant, owner_type, owner_id, id);
CREATE INDEX operations_created_at ON operations (created_at);

-- +goose Down
DROP TABLE operations;
