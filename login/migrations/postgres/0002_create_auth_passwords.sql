-- +goose Up
CREATE TABLE auth_passwords (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    hash text NOT NULL,
    changed_at timestamptz NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);
COMMENT ON TABLE auth_passwords IS '主体的密码哈希（PHC 格式，见 hashing 包）';

-- +goose Down
DROP TABLE auth_passwords;
