-- +goose Up
-- 主体的密码哈希（PHC 格式，见 hashing 包），由 login.Realm 管理。
CREATE TABLE auth_passwords (
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    hash TEXT NOT NULL,
    changed_at TEXT NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);

-- +goose Down
DROP TABLE auth_passwords;
