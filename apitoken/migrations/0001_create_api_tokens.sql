-- +goose Up
CREATE TABLE api_tokens (
    id uuid PRIMARY KEY,
    token_hash text NOT NULL,
    previous_hash text,
    rotated_at timestamptz,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    tenant text NOT NULL,
    impersonator_type text NOT NULL,
    impersonator_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('login', 'refresh', 'personal')),
    name text NOT NULL,
    scopes text NOT NULL,
    idle_timeout bigint NOT NULL,
    ends_at timestamptz,
    ip text NOT NULL,
    user_agent text NOT NULL,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    expires_at timestamptz,
    CONSTRAINT api_tokens_token_hash_unique UNIQUE (token_hash)
);
CREATE INDEX api_tokens_subject ON api_tokens (subject_type, subject_id);
CREATE INDEX api_tokens_previous_hash ON api_tokens (previous_hash) WHERE previous_hash IS NOT NULL;
CREATE INDEX api_tokens_expires_at ON api_tokens (expires_at) WHERE expires_at IS NOT NULL;
COMMENT ON TABLE api_tokens IS 'API token；只存 token 的 SHA-256；过期行由 apitoken.Store.Cleanup 删除';
COMMENT ON COLUMN api_tokens.kind IS 'login：登录时签发，按签发时的配置滑动过期；refresh：JWT 模式的登录，token 是 refresh token，刷新时轮换并滑动过期；personal：个人访问令牌，过期时间由创建者指定';
COMMENT ON COLUMN api_tokens.previous_hash IS 'refresh 轮换前的上一个 token 的 SHA-256，用于识别重复使用；其余为空';
COMMENT ON COLUMN api_tokens.rotated_at IS 'refresh 最近一次轮换的时间；其余为空';
COMMENT ON COLUMN api_tokens.tenant IS 'token 绑定的租户；空字符串表示不绑定';
COMMENT ON COLUMN api_tokens.impersonator_type IS '代为登录时签发 token 的实际操作者，与 impersonator_id 一起引用；空字符串表示用户本人';
COMMENT ON COLUMN api_tokens.scopes IS '个人访问令牌的权限限制，空格分隔；登录 token 为空字符串，表示不限制';
COMMENT ON COLUMN api_tokens.idle_timeout IS '登录 token 签发时确定的空闲时长（秒），使用时据此滑动延长；0 表示不延长，个人访问令牌为 0';
COMMENT ON COLUMN api_tokens.ends_at IS '登录 token 签发时确定的最长期限（签发时间加 lifetime）；个人访问令牌为空';
COMMENT ON COLUMN api_tokens.expires_at IS '实际截止时间；login 在使用时滑动为使用时间加 idle_timeout，不超过 ends_at；personal 为空表示不过期';

-- +goose Down
DROP TABLE api_tokens;
