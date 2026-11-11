-- +goose Up
-- 后台导出生成的文件，状态和进度在 operations 表中，删除操作时级联删除。
-- url 是 completed 时文件的公开地址，文件在 expires_at 之后被回收。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE exports (
    operation_id CHAR(36) NOT NULL PRIMARY KEY,
    format VARCHAR(16) NOT NULL,
    file_id CHAR(36) NULL,
    filename TEXT NOT NULL,
    url TEXT NOT NULL,
    expires_at DATETIME(6) NULL,
    CONSTRAINT exports_operation FOREIGN KEY (operation_id) REFERENCES operations (id) ON DELETE CASCADE
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE exports;
