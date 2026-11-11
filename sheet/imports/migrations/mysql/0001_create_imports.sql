-- +goose Up
-- 后台导入：一个上传的表格文件，状态和进度在 operations 表中，删除操作时级联删除。
-- locale 是发起导入的请求的语言，处理时用它翻译验证提示；header 是文件的表头，开始处理时写入；
-- last_line 是已经处理到的行号，任务重试时从下一行继续。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE imports (
    operation_id CHAR(36) NOT NULL PRIMARY KEY,
    file_id CHAR(36) NOT NULL,
    filename TEXT NOT NULL,
    locale VARCHAR(64) NOT NULL,
    header JSON NOT NULL,
    last_line INT NOT NULL,
    CONSTRAINT imports_operation FOREIGN KEY (operation_id) REFERENCES operations (id) ON DELETE CASCADE
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE imports;
