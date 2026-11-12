-- +goose Up
CREATE TABLE feature_settings (
    name text NOT NULL,
    tenant text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    mode text NOT NULL,
    percent smallint NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (name, tenant, subject_type, subject_id)
);
CREATE INDEX feature_settings_scope ON feature_settings (tenant, subject_type, subject_id);
COMMENT ON TABLE feature_settings IS '功能开关的设置：全员、某个租户或某个主体';
COMMENT ON COLUMN feature_settings.tenant IS '租户的设置为租户 ID，其余为空字符串';
COMMENT ON COLUMN feature_settings.subject_id IS '主体的设置为主体的类型（subject_type）和 ID，其余为空字符串';
COMMENT ON COLUMN feature_settings.mode IS 'on、off，或 percent（只用于全员）';

-- +goose Down
DROP TABLE feature_settings;
