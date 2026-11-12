-- +goose Up
CREATE TABLE auth_identifiers (
    subject_type text NOT NULL,
    tenant text NOT NULL,
    kind text NOT NULL,
    value text NOT NULL,
    subject_id text NOT NULL,
    verified_at timestamptz,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (subject_type, tenant, kind, value)
);
CREATE INDEX auth_identifiers_subject ON auth_identifiers (subject_type, subject_id);
COMMENT ON TABLE auth_identifiers IS '登录标识：用什么找到一个主体；由 login.Realm 管理';
COMMENT ON COLUMN auth_identifiers.tenant IS '标识所在的租户；Realm 为 PerTenant 时标识在租户内唯一，否则为空字符串';
COMMENT ON COLUMN auth_identifiers.kind IS 'email、phone、username，或第三方登录的提供方名称';
COMMENT ON COLUMN auth_identifiers.value IS '规范化后的标识：邮箱小写、手机号 E.164、用户名小写；第三方登录为对方的用户 ID';
COMMENT ON COLUMN auth_identifiers.verified_at IS '通过验证码等方式证明持有的时间；为空表示未验证';

-- +goose Down
DROP TABLE auth_identifiers;
