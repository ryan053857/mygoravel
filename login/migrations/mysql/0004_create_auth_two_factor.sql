-- +goose Up
-- 主体的 TOTP 二次验证，由 login.Realm 管理：secret 是用应用密钥加密的 TOTP 密钥；
-- last_step 是最近一次通过验证的时间步（Unix 秒 / 30），同一个验证码不能重复使用；
-- confirmed_at 为空表示正在设置，尚未启用。
CREATE TABLE auth_two_factor (
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    secret VARCHAR(255) NOT NULL,
    last_step BIGINT NOT NULL,
    confirmed_at DATETIME(6),
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_two_factor;
