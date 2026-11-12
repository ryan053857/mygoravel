-- +goose Up
-- 二次验证的恢复码，每个只能使用一次；code_hash 是恢复码的 HMAC-SHA256，密钥由应用密钥派生。
CREATE TABLE auth_recovery_codes (
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    code_hash CHAR(64) NOT NULL,
    PRIMARY KEY (subject_type, subject_id, code_hash)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_recovery_codes;
