-- +goose Up
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_kind_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_kind_check CHECK (kind IN ('login', 'refresh', 'personal', 'oauth'));
ALTER TABLE api_tokens ADD COLUMN client_id text NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN client_name text NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN resource text NOT NULL DEFAULT '';
COMMENT ON COLUMN api_tokens.client_id IS 'oauth：授权的 OAuth 客户端 ID；其余为空字符串';
COMMENT ON COLUMN api_tokens.client_name IS 'oauth：授权时客户端的名称，用于“已连接的应用”列表；其余为空字符串';
COMMENT ON COLUMN api_tokens.resource IS 'oauth：令牌只能用于这个资源（例如 MCP 服务的地址）；其余为空字符串';

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN resource;
ALTER TABLE api_tokens DROP COLUMN client_name;
ALTER TABLE api_tokens DROP COLUMN client_id;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_kind_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_kind_check CHECK (kind IN ('login', 'refresh', 'personal'));
