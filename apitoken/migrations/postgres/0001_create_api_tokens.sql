-- +goose Up
CREATE TABLE api_tokens (
    id uuid PRIMARY KEY,
    token_hash text NOT NULL,
    subject_id text NOT NULL,
    subject_type text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('login', 'personal')),
    name text NOT NULL,
    scopes text NOT NULL,
    remember boolean NOT NULL,
    ip text NOT NULL,
    user_agent text NOT NULL,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    expires_at timestamptz,
    CONSTRAINT api_tokens_token_hash_unique UNIQUE (token_hash)
);
CREATE INDEX api_tokens_subject_id ON api_tokens (subject_id);
CREATE INDEX api_tokens_expires_at ON api_tokens (expires_at) WHERE expires_at IS NOT NULL;
COMMENT ON TABLE api_tokens IS 'API token；只存 token 的 SHA-256；过期行由 apitoken.Store.Cleanup 删除';
COMMENT ON COLUMN api_tokens.kind IS 'login：登录时签发，按配置滑动过期；personal：个人访问令牌，过期时间由创建者指定';
COMMENT ON COLUMN api_tokens.scopes IS '空格分隔的 scope';
COMMENT ON COLUMN api_tokens.expires_at IS '实际截止时间；login 在使用时滑动延长，不超过创建时间加 lifetime；personal 为空表示不过期';

-- +goose Down
DROP TABLE api_tokens;
