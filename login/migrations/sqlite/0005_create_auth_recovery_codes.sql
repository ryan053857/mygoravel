-- +goose Up
-- 二次验证的恢复码，每个只能使用一次；code_hash 是恢复码的 HMAC-SHA256，密钥由应用密钥派生。
CREATE TABLE auth_recovery_codes (
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    PRIMARY KEY (subject_type, subject_id, code_hash)
);

-- +goose Down
DROP TABLE auth_recovery_codes;
