-- +goose Up
ALTER TABLE api_tokens ADD COLUMN auth_methods text NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN auth_time timestamptz;
COMMENT ON COLUMN api_tokens.auth_methods IS '登录 token 这次登录通过的验证方式（空格分隔），如 password totp；确认身份时补充；其他种类为空';
COMMENT ON COLUMN api_tokens.auth_time IS '登录 token 最近一次验证身份的时间：登录或确认身份；为空表示不经过本人登录（如代为登录）';

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN auth_time;
ALTER TABLE api_tokens DROP COLUMN auth_methods;
