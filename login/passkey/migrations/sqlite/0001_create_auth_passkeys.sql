-- +goose Up
-- passkey 的公钥，由 login/passkey 管理；属于哪个主体由 auth_identifiers 中 kind 为 passkey 的标识决定。
-- credential_id 是 credential ID 的 URL 安全 base64，也是对应登录标识的 value；
-- credential 是 go-webauthn 的 Credential（JSON）：公钥、签名计数、标志位、传输方式；name 是用户可见的名称。
-- 时间是 Bun 写入的 UTC 文本。
CREATE TABLE auth_passkeys (
    credential_id TEXT PRIMARY KEY,
    subject_type TEXT NOT NULL,
    credential TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX auth_passkeys_created_at ON auth_passkeys (created_at);

-- +goose Down
DROP TABLE auth_passkeys;
