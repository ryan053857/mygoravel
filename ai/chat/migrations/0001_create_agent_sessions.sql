-- +goose Up
-- trpc-agent-go session/postgres 的表结构，表名前缀 agent_；由 ai/postgres.NewSessions 使用。
CREATE TABLE agent_session_states (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    user_id varchar(255) NOT NULL,
    session_id varchar(255) NOT NULL,
    state jsonb,
    created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE UNIQUE INDEX idx_agent_session_states_unique_active ON agent_session_states (app_name, user_id, session_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_session_states_expires ON agent_session_states (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_events (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    user_id varchar(255) NOT NULL,
    session_id varchar(255) NOT NULL,
    event jsonb NOT NULL,
    created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE INDEX idx_agent_session_events_lookup ON agent_session_events (app_name, user_id, session_id, created_at);
CREATE INDEX idx_agent_session_events_expires ON agent_session_events (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_track_events (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    user_id varchar(255) NOT NULL,
    session_id varchar(255) NOT NULL,
    track varchar(255) NOT NULL,
    event jsonb NOT NULL,
    created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE INDEX idx_agent_session_track_events_lookup ON agent_session_track_events (app_name, user_id, session_id, track, created_at);
CREATE INDEX idx_agent_session_track_events_expires ON agent_session_track_events (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_summaries (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    user_id varchar(255) NOT NULL,
    session_id varchar(255) NOT NULL,
    filter_key varchar(255) NOT NULL DEFAULT '',
    summary jsonb,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE UNIQUE INDEX idx_agent_session_summaries_unique_active ON agent_session_summaries (app_name, user_id, session_id, filter_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_session_summaries_expires ON agent_session_summaries (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_app_states (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    key varchar(255) NOT NULL,
    value text,
    created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE UNIQUE INDEX idx_agent_app_states_unique_active ON agent_app_states (app_name, key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_app_states_expires ON agent_app_states (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_user_states (
    id bigserial PRIMARY KEY,
    app_name varchar(255) NOT NULL,
    user_id varchar(255) NOT NULL,
    key varchar(255) NOT NULL,
    value text,
    created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp,
    deleted_at timestamp
);
CREATE UNIQUE INDEX idx_agent_user_states_unique_active ON agent_user_states (app_name, user_id, key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_user_states_expires ON agent_user_states (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE agent_user_states;
DROP TABLE agent_app_states;
DROP TABLE agent_session_summaries;
DROP TABLE agent_session_track_events;
DROP TABLE agent_session_events;
DROP TABLE agent_session_states;
