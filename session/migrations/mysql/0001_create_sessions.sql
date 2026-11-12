-- +goose Up
-- scs 会话；subject_type、subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除。
-- id 是登录时生成的会话 ID，即主体的 Credential；impersonator_type、impersonator_id 是代为登录时实际操作的人；ip、user_agent、logged_in_at 是登录设备和时间，未登录时为空。
-- utf8mb4_bin 让 token 区分大小写：默认排序规则下仅大小写不同的 token 会被视为同一个会话。
CREATE TABLE sessions (
    token VARCHAR(255) NOT NULL PRIMARY KEY,
    subject_type VARCHAR(64),
    subject_id VARCHAR(191),
    id CHAR(36),
    impersonator_type VARCHAR(64) NOT NULL,
    impersonator_id VARCHAR(191) NOT NULL,
    ip VARCHAR(64) NOT NULL,
    user_agent VARCHAR(255) NOT NULL,
    logged_in_at DATETIME(6),
    data MEDIUMBLOB NOT NULL,
    expiry DATETIME(6) NOT NULL,
    revoked_at DATETIME(6),
    INDEX sessions_expiry (expiry),
    INDEX sessions_subject (subject_type, subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE sessions;
