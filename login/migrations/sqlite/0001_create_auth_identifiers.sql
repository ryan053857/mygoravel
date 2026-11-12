-- +goose Up
-- 登录标识，由 login.Realm 管理：kind 为 email、phone、username 或第三方登录的提供方名称，value 是规范化后的值；
-- Realm 为 PerTenant 时标识在租户内唯一，否则 tenant 为空字符串；verified_at 为空表示未验证。时间是 Bun 写入的 UTC 文本。
CREATE TABLE auth_identifiers (
    subject_type TEXT NOT NULL,
    tenant TEXT NOT NULL,
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    verified_at TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (subject_type, tenant, kind, value)
);
CREATE INDEX auth_identifiers_subject ON auth_identifiers (subject_type, subject_id);

-- +goose Down
DROP TABLE auth_identifiers;
