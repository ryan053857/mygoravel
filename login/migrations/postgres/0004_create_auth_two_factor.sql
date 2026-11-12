-- +goose Up
CREATE TABLE auth_two_factor (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    secret text NOT NULL,
    last_step bigint NOT NULL,
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);
COMMENT ON TABLE auth_two_factor IS '主体的 TOTP 二次验证，由 login.Realm 管理';
COMMENT ON COLUMN auth_two_factor.secret IS 'TOTP 密钥，用应用密钥加密（crypt.Encrypt）';
COMMENT ON COLUMN auth_two_factor.last_step IS '最近一次通过验证的时间步（Unix 秒 / 30），同一个验证码不能重复使用';
COMMENT ON COLUMN auth_two_factor.confirmed_at IS '用户输入验证码确认绑定的时间；为空表示正在设置，尚未启用';

-- +goose Down
DROP TABLE auth_two_factor;
