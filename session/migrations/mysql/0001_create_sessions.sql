-- +goose Up
-- scs 会话；subject_id 是登录主体，revoked_at 非空表示已撤销；过期行由 session.Store.Cleanup 删除。
-- utf8mb4_bin 让 token 区分大小写：默认排序规则下仅大小写不同的 token 会被视为同一个会话。
CREATE TABLE sessions (
    token VARCHAR(255) NOT NULL PRIMARY KEY,
    subject_id VARCHAR(255),
    data MEDIUMBLOB NOT NULL,
    expiry DATETIME(6) NOT NULL,
    revoked_at DATETIME(6),
    INDEX sessions_expiry (expiry),
    INDEX sessions_subject_id (subject_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE sessions;
