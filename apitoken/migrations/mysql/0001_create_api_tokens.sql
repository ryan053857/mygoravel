-- +goose Up
-- API token；只存 token 的 SHA-256；过期行由 apitoken.Store.Cleanup 删除。
-- kind：login 登录时签发，按配置滑动过期；refresh 是 JWT 模式的登录，token 是 refresh token，刷新时轮换并滑动过期；personal 个人访问令牌，过期时间由创建者指定。
-- previous_hash、rotated_at 是 refresh 轮换前的上一个 token 的 SHA-256 和最近一次轮换的时间，用于识别重复使用；其余为空。
-- tenant 是 token 绑定的租户，空字符串表示不绑定。impersonator_type、impersonator_id 是代为登录时的实际操作者，空字符串表示用户本人。scopes 是个人访问令牌的权限限制，空格分隔；登录 token 为空字符串，表示不限制。idle_timeout（秒）和 ends_at 是登录 token 签发时确定的空闲时长和最长期限，个人访问令牌为 0 和空。expires_at 是实际截止时间：login 在使用时滑动为“使用时间 + idle_timeout”，不超过 ends_at；personal 为空表示不过期。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
-- utf8mb4_bin 让字符串比较区分大小写，与 PostgreSQL 和 SQLite 一致。
CREATE TABLE api_tokens (
    id CHAR(36) NOT NULL PRIMARY KEY,
    token_hash CHAR(64) NOT NULL,
    previous_hash CHAR(64),
    rotated_at DATETIME(6),
    subject_type VARCHAR(64) NOT NULL,
    subject_id VARCHAR(191) NOT NULL,
    tenant VARCHAR(255) NOT NULL,
    impersonator_type VARCHAR(64) NOT NULL,
    impersonator_id VARCHAR(191) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('login', 'refresh', 'personal')),
    name VARCHAR(255) NOT NULL,
    scopes TEXT NOT NULL,
    idle_timeout BIGINT NOT NULL,
    ends_at DATETIME(6),
    ip VARCHAR(64) NOT NULL,
    user_agent VARCHAR(255) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    last_used_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6),
    CONSTRAINT api_tokens_token_hash_unique UNIQUE (token_hash),
    INDEX api_tokens_subject (subject_type, subject_id),
    INDEX api_tokens_previous_hash (previous_hash),
    INDEX api_tokens_expires_at (expires_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE api_tokens;
