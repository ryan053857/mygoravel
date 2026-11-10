-- +goose Up
CREATE TABLE files (
    id uuid PRIMARY KEY,
    disk text NOT NULL,
    key text NOT NULL,
    filename text NOT NULL,
    content_type text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    owner_type text NOT NULL,
    owner_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('temporary', 'ready', 'deleted')),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT files_disk_key_unique UNIQUE (disk, key)
);
CREATE INDEX files_owner ON files (owner_type, owner_id);
CREATE INDEX files_expires_at ON files (expires_at) WHERE expires_at IS NOT NULL;
COMMENT ON TABLE files IS '已上传文件；公开地址 = 磁盘 public_url + key';
COMMENT ON COLUMN files.disk IS '应用装配时的磁盘名，不是驱动名';
COMMENT ON COLUMN files.expires_at IS 'temporary 未确认的回收时间，deleted 的对象清理重试时间；ready 为空';

-- +goose Down
DROP TABLE files;
