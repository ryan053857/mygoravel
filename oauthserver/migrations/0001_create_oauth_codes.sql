-- +goose Up
CREATE TABLE oauth_codes (
    code_hash text PRIMARY KEY,
    client_id text NOT NULL,
    client_name text NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    scopes text NOT NULL,
    resource text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    tenant text NOT NULL,
    impersonator_type text NOT NULL,
    impersonator_id text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX oauth_codes_expires_at ON oauth_codes (expires_at);
COMMENT ON TABLE oauth_codes IS 'OAuth 授权码：用户同意后签发，换取令牌时删除，只能使用一次；过期的由 oauthserver.Server.Cleanup 删除';
COMMENT ON COLUMN oauth_codes.code_hash IS '授权码的 SHA-256，授权码本身不保存';
COMMENT ON COLUMN oauth_codes.code_challenge IS 'PKCE 的 code_challenge（S256）';
COMMENT ON COLUMN oauth_codes.scopes IS '用户同意的范围，空格分隔';
COMMENT ON COLUMN oauth_codes.resource IS '令牌将要用于的资源（RFC 8707）';
COMMENT ON COLUMN oauth_codes.tenant IS '授权时主体所在的租户，空字符串为平台本身';

-- +goose Down
DROP TABLE oauth_codes;
