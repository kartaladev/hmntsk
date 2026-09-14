-- MySQL schema for notify's optional email delivery records, 8.0 or later.
--
-- A host that emails notifications applies this document in addition to the
-- notification store's own; a host that does not never needs it.
--
-- One row per notification that a dispatcher has considered. There is no
-- foreign key to the notifications table: retention deletes notifications in
-- batches on every dialect, and the dispatcher removes the records it leaves
-- behind.
--
-- Identifier columns are pinned to utf8mb4_0900_as_cs and timestamps are
-- DATETIME(6), as in the notification store's schema. MySQL has no CREATE INDEX
-- IF NOT EXISTS, so indexes are declared inside the table.

CREATE TABLE IF NOT EXISTS `{{PREFIX}}notify_email_deliveries` (
    `notification_id`  VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `recipient`        VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,
    `status`           VARCHAR(16)  COLLATE utf8mb4_0900_as_cs NOT NULL,
    `batch_id`         VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NULL,
    `owner`            VARCHAR(255) COLLATE utf8mb4_0900_as_cs NULL,
    `lease_until`      DATETIME(6) NULL,
    `attempts`         INT NOT NULL,
    `next_attempt_at`  DATETIME(6) NULL,
    `reason`           LONGTEXT NULL,
    `sent_at`          DATETIME(6) NULL,
    `updated_at`       DATETIME(6) NOT NULL,
    PRIMARY KEY (`notification_id`),
    KEY `{{PREFIX}}notify_email_deliveries_lease_idx` (`status`, `lease_until`),
    KEY `{{PREFIX}}notify_email_deliveries_retry_idx` (`status`, `next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
