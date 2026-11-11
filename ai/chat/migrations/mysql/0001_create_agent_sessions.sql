-- +goose Up
-- trpc-agent-go session/mysql 的表结构，表名前缀 agent_；由 ai/mysql.NewSessions 使用。
CREATE TABLE `agent_session_states` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `user_id` varchar(255) NOT NULL,
  `session_id` varchar(255) NOT NULL,
  `state` json DEFAULT NULL,
  `created_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_agent_session_states_unique_active` (`app_name`,`user_id`,`session_id`,`deleted_at`),
  KEY `idx_agent_session_states_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `agent_session_events` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `user_id` varchar(255) NOT NULL,
  `session_id` varchar(255) NOT NULL,
  `event` json NOT NULL,
  `created_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_agent_session_events_lookup` (`app_name`,`user_id`,`session_id`,`created_at`),
  KEY `idx_agent_session_events_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `agent_session_track_events` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `user_id` varchar(255) NOT NULL,
  `session_id` varchar(255) NOT NULL,
  `track` varchar(255) NOT NULL,
  `event` json NOT NULL,
  `created_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_agent_session_track_events_lookup` (`app_name`,`user_id`,`session_id`,`created_at`),
  KEY `idx_agent_session_track_events_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `agent_session_summaries` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `user_id` varchar(255) NOT NULL,
  `session_id` varchar(255) NOT NULL,
  `filter_key` varchar(255) NOT NULL DEFAULT '',
  `summary` json DEFAULT NULL,
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_agent_session_summaries_unique_active` (`app_name`(191),`user_id`(191),`session_id`(191),`filter_key`(191)),
  KEY `idx_agent_session_summaries_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `agent_app_states` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `key` varchar(255) NOT NULL,
  `value` text,
  `created_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_agent_app_states_unique_active` (`app_name`,`key`,`deleted_at`),
  KEY `idx_agent_app_states_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `agent_user_states` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `app_name` varchar(255) NOT NULL,
  `user_id` varchar(255) NOT NULL,
  `key` varchar(255) NOT NULL,
  `value` text,
  `created_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` timestamp(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  `expires_at` timestamp(6) NULL DEFAULT NULL,
  `deleted_at` timestamp(6) NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_agent_user_states_unique_active` (`app_name`,`user_id`,`key`,`deleted_at`),
  KEY `idx_agent_user_states_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE `agent_user_states`;
DROP TABLE `agent_app_states`;
DROP TABLE `agent_session_summaries`;
DROP TABLE `agent_session_track_events`;
DROP TABLE `agent_session_events`;
DROP TABLE `agent_session_states`;
