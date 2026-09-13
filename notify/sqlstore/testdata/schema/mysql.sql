-- MySQL schema for the notify notification store, 8.0 or later.
--
-- Identifier columns are pinned to utf8mb4_0900_as_cs. The server default,
-- utf8mb4_0900_ai_ci, is case-insensitive, which would deliver a notification
-- for 'alice' to 'Alice' on this one dialect out of three.
--
-- Identifier columns are VARCHAR rather than TEXT because MySQL cannot index a
-- TEXT column without a prefix length. Every index here stays within InnoDB's
-- 3072-byte key limit at four bytes a character.
--
-- Timestamps are DATETIME(6), not TIMESTAMP: TIMESTAMP converts through the
-- session time zone and runs out of range in 2038.
--
-- links and data are LONGTEXT, not JSON. MySQL's JSON type sorts object keys
-- and rewrites number literals, and the store promises to return a publisher's
-- payload exactly as it was published.
--
-- MySQL has no CREATE INDEX IF NOT EXISTS, so indexes are declared inside each
-- table.
--
-- Table and index names carry the host's configured prefix, applied when this
-- document is rendered; with no prefix configured they are exactly as written.

CREATE TABLE IF NOT EXISTS `notify_notifications` (
    `id`               VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `recipient`        VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `source_id`        VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `subject`          VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `subject_version`  BIGINT NOT NULL,
    `kind`             VARCHAR(100) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `state`            VARCHAR(16)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `closed_reason`    VARCHAR(255) NULL,
    `title`            LONGTEXT NULL,
    `links`            LONGTEXT NULL,
    `data`             LONGTEXT NULL,
    `created_at`       DATETIME(6) NOT NULL,
    `read_at`          DATETIME(6) NULL,
    `closed_at`        DATETIME(6) NULL,
    `inactive_at`      DATETIME(6) NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `notify_notifications_source_key` (`source_id`, `recipient`),
    KEY `notify_notifications_recipient_idx` (`recipient`, `created_at`, `id`),
    KEY `notify_notifications_state_idx` (`recipient`, `state`),
    KEY `notify_notifications_subject_idx` (`subject`, `kind`, `state`),
    KEY `notify_notifications_inactive_idx` (`state`, `inactive_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A subject's close records: the highest version closed per kind, and per
-- every kind under kind '*'. Publishing and closing a subject both lock its '*'
-- row first, which is what serialises them.
CREATE TABLE IF NOT EXISTS `notify_watermarks` (
    `subject`     VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `kind`        VARCHAR(100) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `version`     BIGINT NOT NULL,
    `updated_at`  DATETIME(6) NOT NULL,
    PRIMARY KEY (`subject`, `kind`),
    KEY `notify_watermarks_updated_idx` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
