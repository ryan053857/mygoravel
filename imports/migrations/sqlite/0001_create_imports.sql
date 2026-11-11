-- +goose Up
-- 后台导入：一个上传的表格文件，状态和进度在 operations 表中，删除操作时级联删除。
-- locale 是发起导入的请求的语言，处理时用它翻译验证提示；header 是文件的表头（JSON 文本），开始处理时写入；
-- last_line 是已经处理到的行号，任务重试时从下一行继续。id 是 UUID 文本。
CREATE TABLE imports (
    operation_id TEXT PRIMARY KEY REFERENCES operations (id) ON DELETE CASCADE,
    file_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    locale TEXT NOT NULL,
    header TEXT NOT NULL,
    last_line INTEGER NOT NULL
);

-- +goose Down
DROP TABLE imports;
