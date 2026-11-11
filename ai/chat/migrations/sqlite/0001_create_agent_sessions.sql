-- +goose Up
-- trpc-agent-go session/sqlite 的表结构，表名前缀 agent_；由 ai/sqlite.NewSessions 使用。时间是 Unix 纳秒。
CREATE TABLE agent_session_states (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  state BLOB DEFAULT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE UNIQUE INDEX idx_agent_session_states_unique_active ON agent_session_states(app_name, user_id, session_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_session_states_expires ON agent_session_states(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  event BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE INDEX idx_agent_session_events_lookup ON agent_session_events(app_name, user_id, session_id, created_at);
CREATE INDEX idx_agent_session_events_expires ON agent_session_events(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_track_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  track TEXT NOT NULL,
  event BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE INDEX idx_agent_session_track_events_lookup ON agent_session_track_events(app_name, user_id, session_id, track, created_at);
CREATE INDEX idx_agent_session_track_events_expires ON agent_session_track_events(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_session_summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  filter_key TEXT NOT NULL DEFAULT '',
  summary BLOB DEFAULT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE UNIQUE INDEX idx_agent_session_summaries_unique_active ON agent_session_summaries(app_name, user_id, session_id, filter_key);
CREATE INDEX idx_agent_session_summaries_expires ON agent_session_summaries(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_app_states (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  key TEXT NOT NULL,
  value BLOB DEFAULT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE UNIQUE INDEX idx_agent_app_states_unique_active ON agent_app_states(app_name, key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_app_states_expires ON agent_app_states(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE agent_user_states (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value BLOB DEFAULT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER DEFAULT NULL,
  deleted_at INTEGER DEFAULT NULL
);
CREATE UNIQUE INDEX idx_agent_user_states_unique_active ON agent_user_states(app_name, user_id, key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_user_states_expires ON agent_user_states(expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE agent_user_states;
DROP TABLE agent_app_states;
DROP TABLE agent_session_summaries;
DROP TABLE agent_session_track_events;
DROP TABLE agent_session_events;
DROP TABLE agent_session_states;
