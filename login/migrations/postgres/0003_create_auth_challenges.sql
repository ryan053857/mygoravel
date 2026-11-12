-- +goose Up
CREATE TABLE auth_challenges (
    id uuid PRIMARY KEY,
    purpose text NOT NULL,
    subject_type text NOT NULL,
    tenant text NOT NULL,
    kind text NOT NULL,
    value text NOT NULL,
    subject_id text NOT NULL,
    code_hash text NOT NULL,
    attempts integer NOT NULL,
    proof_hash text,
    completed_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX auth_challenges_subject ON auth_challenges (subject_type, subject_id);
CREATE INDEX auth_challenges_expires_at ON auth_challenges (expires_at);
COMMENT ON TABLE auth_challenges IS '验证码与完成凭证；过期行由 login.Realm.Cleanup 删除';
COMMENT ON COLUMN auth_challenges.purpose IS 'sign_in、reset、verify，或 two_factor（密码等验证通过、等待二次验证的登录）';
COMMENT ON COLUMN auth_challenges.subject_id IS '发送验证码时标识所属的主体；空字符串表示标识尚未注册';
COMMENT ON COLUMN auth_challenges.code_hash IS '验证码的 HMAC-SHA256，密钥由应用密钥派生';
COMMENT ON COLUMN auth_challenges.attempts IS '已尝试的次数';
COMMENT ON COLUMN auth_challenges.proof_hash IS '验证通过后签发的完成凭证的 SHA-256；为空表示尚未验证';
COMMENT ON COLUMN auth_challenges.completed_at IS '验证码登录完成或完成凭证被使用的时间';
COMMENT ON COLUMN auth_challenges.expires_at IS '验证码的截止时间；验证通过后改为完成凭证的截止时间';

-- +goose Down
DROP TABLE auth_challenges;
