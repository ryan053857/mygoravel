-- +goose Up
-- 验证码与完成凭证，由 login.Realm 管理：purpose 为 sign_in、reset、verify，或 two_factor（等待二次验证的登录）；subject_id 为空表示标识尚未注册；
-- code_hash 是验证码的 HMAC-SHA256；proof_hash 是验证通过后签发的完成凭证的 SHA-256；
-- expires_at 在验证通过后改为完成凭证的截止时间；过期行由 Realm.Cleanup 删除。
CREATE TABLE auth_challenges (
    id CHAR(36) NOT NULL PRIMARY KEY,
    purpose VARCHAR(16) NOT NULL,
    subject_type VARCHAR(64) NOT NULL,
    tenant VARCHAR(255) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    value VARCHAR(191) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    code_hash CHAR(64) NOT NULL,
    attempts INT NOT NULL,
    proof_hash CHAR(64),
    completed_at DATETIME(6),
    expires_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    INDEX auth_challenges_subject (subject_type, subject_id),
    INDEX auth_challenges_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_challenges;
