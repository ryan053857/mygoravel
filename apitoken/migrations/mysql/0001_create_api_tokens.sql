-- +goose Up
-- API token；只存 token 的 SHA-256；过期行由 apitoken.Store.Cleanup 删除。
-- kind：login 登录时签发，按配置滑动过期；personal 个人访问令牌，过期时间由创建者指定。
-- tenant 是 token 绑定的租户，空字符串表示不绑定。scopes 是个人访问令牌的权限限制，空格分隔；登录 token 为空字符串，表示不限制。expires_at 是实际截止时间，personal 为空表示不过期。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE api_tokens (
    id CHAR(36) NOT NULL PRIMARY KEY,
    token_hash CHAR(64) NOT NULL,
    subject_id VARCHAR(255) NOT NULL,
    subject_type VARCHAR(64) NOT NULL,
    tenant VARCHAR(255) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('login', 'personal')),
    name VARCHAR(255) NOT NULL,
    scopes TEXT NOT NULL,
    remember BOOLEAN NOT NULL,
    ip VARCHAR(64) NOT NULL,
    user_agent VARCHAR(255) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    last_used_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6),
    CONSTRAINT api_tokens_token_hash_unique UNIQUE (token_hash),
    INDEX api_tokens_subject_id (subject_id),
    INDEX api_tokens_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE api_tokens;
