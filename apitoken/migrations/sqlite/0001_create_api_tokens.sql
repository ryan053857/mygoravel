-- +goose Up
-- API token；只存 token 的 SHA-256；过期行由 apitoken.Store.Cleanup 删除。
-- kind：login 登录时签发，按配置滑动过期；personal 个人访问令牌，过期时间由创建者指定。
-- tenant 是 token 绑定的租户，空字符串表示不绑定。impersonator 是代为登录时的实际操作者 ID，空字符串表示用户本人。scopes 是个人访问令牌的权限限制，空格分隔；登录 token 为空字符串，表示不限制。expires_at 是实际截止时间，personal 为空表示不过期。
-- id 是 UUID 文本，时间是 Bun 写入的 UTC 文本，remember 为 0 或 1。
CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    tenant TEXT NOT NULL,
    impersonator TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('login', 'personal')),
    name TEXT NOT NULL,
    scopes TEXT NOT NULL,
    remember INTEGER NOT NULL,
    ip TEXT NOT NULL,
    user_agent TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    expires_at TEXT,
    CONSTRAINT api_tokens_token_hash_unique UNIQUE (token_hash)
);
CREATE INDEX api_tokens_subject_id ON api_tokens (subject_id);
CREATE INDEX api_tokens_expires_at ON api_tokens (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE api_tokens;
