-- +goose Up
-- 验证码与完成凭证，由 login.Realm 管理：purpose 为 sign_in、reset、verify，external（第三方账号尚未绑定）或 two_factor（等待二次验证的登录）；subject_id 为空表示标识尚未注册；
-- code_hash 是验证码的 HMAC-SHA256；proof_hash 是验证通过后签发的完成凭证的 SHA-256；data 是流程附带的数据（JSON）；
-- expires_at 在验证通过后改为完成凭证的截止时间；过期行由 Realm.Cleanup 删除。id 是 UUID 文本，时间是 Bun 写入的 UTC 文本。
CREATE TABLE auth_challenges (
    id TEXT PRIMARY KEY,
    purpose TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    tenant TEXT NOT NULL,
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    proof_hash TEXT,
    data TEXT,
    completed_at TEXT,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX auth_challenges_subject ON auth_challenges (subject_type, subject_id);
CREATE INDEX auth_challenges_expires_at ON auth_challenges (expires_at);

-- +goose Down
DROP TABLE auth_challenges;
