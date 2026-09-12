-- MySQL schema for the hmntsk engine, 8.0 or later.
--
-- Every identifier column is pinned to utf8mb4_0900_as_cs. The server default,
-- utf8mb4_0900_ai_ci, is case-insensitive, which would make an actor called
-- 'alice' match a candidate called 'Alice' — silently changing who may claim a
-- task, and only on this one dialect out of three.
--
-- Timestamps are DATETIME(6), not TIMESTAMP: TIMESTAMP converts through the
-- session time zone and runs out of range in 2038.
--
-- Identifier columns are VARCHAR rather than TEXT because MySQL cannot index a
-- TEXT column without a prefix length.
--
-- Payload columns are LONGTEXT, not JSON. MySQL's JSON type sorts object keys
-- and rewrites number literals, and the engine promises to return a payload
-- exactly as it was supplied. Nothing queries inside a payload.
--
-- {{PREFIX}} is replaced with the host's configured table prefix.

CREATE TABLE IF NOT EXISTS `{{PREFIX}}tasks` (
    `id`                 VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `task_type`          VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `version`            BIGINT NOT NULL,
    `status`             VARCHAR(32)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `suspended_from`     VARCHAR(32)  COLLATE utf8mb4_0900_as_cs NULL,
    `priority`           INT NOT NULL,
    `assignee`           VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `owner_type`         VARCHAR(128) COLLATE utf8mb4_0900_as_cs NULL,
    `owner_ref`          VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `activity_key`       VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `correlation_extra`  LONGTEXT NULL,
    `callback_address`   TEXT NULL,
    `callback_params`    LONGTEXT NULL,
    `escalation`         LONGTEXT NULL,
    `input`              LONGTEXT NULL,
    `progress`           LONGTEXT NULL,
    `output`             LONGTEXT NULL,
    `reason`             TEXT NULL,
    `created_by`         VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `escalation_count`   INT NOT NULL DEFAULT 0,
    `created_at`         DATETIME(6) NOT NULL,
    `updated_at`         DATETIME(6) NOT NULL,
    `due_at`             DATETIME(6) NULL,
    `started_at`         DATETIME(6) NULL,
    `closed_at`          DATETIME(6) NULL,
    `escalated_at`       DATETIME(6) NULL,
    `locked_by`          VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `locked_until`       DATETIME(6) NULL,
    PRIMARY KEY (`id`),
    KEY `{{PREFIX}}tasks_assignee_idx` (`assignee`, `id`),
    KEY `{{PREFIX}}tasks_status_idx` (`status`, `id`),
    KEY `{{PREFIX}}tasks_type_idx` (`task_type`, `id`),
    KEY `{{PREFIX}}tasks_correlation_idx` (`owner_type`, `owner_ref`, `activity_key`),
    KEY `{{PREFIX}}tasks_due_idx` (`due_at`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `{{PREFIX}}task_candidates` (
    `task_id`  VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `kind`     VARCHAR(16)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `value`    VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `ordinal`  INT NOT NULL DEFAULT 0,
    PRIMARY KEY (`task_id`, `kind`, `value`),
    KEY `{{PREFIX}}task_candidates_lookup_idx` (`kind`, `value`, `task_id`),
    CONSTRAINT `{{PREFIX}}task_candidates_task_fk` FOREIGN KEY (`task_id`)
        REFERENCES `{{PREFIX}}tasks` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `{{PREFIX}}task_history` (
    `task_id`      VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `version`      BIGINT NOT NULL,
    `operation`    VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `from_status`  VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `to_status`    VARCHAR(32) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `actor`        VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `comment`      TEXT NULL,
    `at`           DATETIME(6) NOT NULL,
    PRIMARY KEY (`task_id`, `version`),
    CONSTRAINT `{{PREFIX}}task_history_task_fk` FOREIGN KEY (`task_id`)
        REFERENCES `{{PREFIX}}tasks` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `{{PREFIX}}task_outbox` (
    `id`            VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `task_id`       VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `task_type`     VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `event_type`    VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `occurred_at`   DATETIME(6) NOT NULL,
    `published_at`  DATETIME(6) NULL,
    `payload`       LONGTEXT NOT NULL,
    PRIMARY KEY (`id`),
    KEY `{{PREFIX}}task_outbox_unpublished_idx` (`published_at`, `occurred_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `{{PREFIX}}task_types` (
    `name`                VARCHAR(128) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `title`               TEXT NULL,
    `description`         TEXT NULL,
    `input_schema`        LONGTEXT NULL,
    `output_schema`       LONGTEXT NULL,
    `default_priority`    INT NOT NULL DEFAULT 5,
    `default_deadline_ms` BIGINT NOT NULL DEFAULT 0,
    `default_escalation`  LONGTEXT NULL,
    `default_assignment`  LONGTEXT NULL,
    `updated_at`          DATETIME(6) NOT NULL,
    PRIMARY KEY (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
