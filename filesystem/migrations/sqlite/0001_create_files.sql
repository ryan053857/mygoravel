-- +goose Up
-- 已上传文件；公开地址 = 磁盘 public_url + key。disk 是应用装配时的磁盘名，不是驱动名。
-- expires_at：temporary 未确认的回收时间，deleted 等待清理任务删除对象的时间；ready 为空。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE files (
    id TEXT PRIMARY KEY,
    disk TEXT NOT NULL,
    key TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('temporary', 'ready', 'deleted')),
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CONSTRAINT files_disk_key_unique UNIQUE (disk, key)
);
CREATE INDEX files_owner ON files (owner_type, owner_id);
CREATE INDEX files_expires_at ON files (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE files;
