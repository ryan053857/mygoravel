-- +goose Up
-- 主体的 TOTP 二次验证，由 login.Realm 管理：secret 是用应用密钥加密的 TOTP 密钥；
-- last_step 是最近一次通过验证的时间步（Unix 秒 / 30），同一个验证码不能重复使用；
-- confirmed_at 为空表示正在设置，尚未启用。时间是 Bun 写入的 UTC 文本。
CREATE TABLE auth_two_factor (
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    secret TEXT NOT NULL,
    last_step INTEGER NOT NULL,
    confirmed_at TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);

-- +goose Down
DROP TABLE auth_two_factor;
