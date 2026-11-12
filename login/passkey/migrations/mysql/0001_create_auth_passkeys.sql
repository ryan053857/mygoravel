-- +goose Up
-- passkey 的公钥，由 login/passkey 管理；属于哪个主体由 auth_identifiers 中 kind 为 passkey 的标识决定。
-- credential_id 是 credential ID 的 URL 安全 base64，也是对应登录标识的 value；
-- credential 是 go-webauthn 的 Credential（JSON）：公钥、签名计数、标志位、传输方式；name 是用户可见的名称。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
CREATE TABLE auth_passkeys (
    credential_id VARCHAR(191) NOT NULL PRIMARY KEY,
    subject_type VARCHAR(64) NOT NULL,
    credential TEXT NOT NULL,
    name VARCHAR(255) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    last_used_at DATETIME(6),
    INDEX auth_passkeys_created_at (created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_passkeys;
