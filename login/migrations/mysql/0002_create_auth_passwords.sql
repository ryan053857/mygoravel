-- +goose Up
-- 主体的密码哈希（PHC 格式，见 hashing 包），由 login.Realm 管理。
CREATE TABLE auth_passwords (
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    hash VARCHAR(255) NOT NULL,
    changed_at DATETIME(6) NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE auth_passwords;
