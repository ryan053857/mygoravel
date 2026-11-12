-- +goose Up
-- 功能开关的设置：全员（tenant、subject_type、subject_id 都为空字符串）、某个租户或某个主体。
-- mode 为 on、off，或 percent（只用于全员）。
CREATE TABLE feature_settings (
    name TEXT NOT NULL,
    tenant TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    percent INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (name, tenant, subject_type, subject_id)
);
CREATE INDEX feature_settings_scope ON feature_settings (tenant, subject_type, subject_id);

-- +goose Down
DROP TABLE feature_settings;
