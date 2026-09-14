-- SQLite schema for notify's optional email delivery records, 3.35 or later.
--
-- A host that emails notifications applies this document in addition to the
-- notification store's own; a host that does not never needs it.
--
-- One row per notification that a dispatcher has considered. There is no
-- foreign key to the notifications table: SQLite enforces foreign keys only
-- behind a per-connection pragma, and the dispatcher removes the records
-- retention leaves behind.
--
-- Instants are TEXT in the one fixed encoding the notification store uses.

CREATE TABLE IF NOT EXISTS "notify_email_deliveries" (
    "notification_id"  TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "recipient"        TEXT COLLATE BINARY NOT NULL,
    "status"           TEXT COLLATE BINARY NOT NULL,
    "batch_id"         TEXT COLLATE BINARY,
    "owner"            TEXT COLLATE BINARY,
    "lease_until"      TEXT,
    "attempts"         INTEGER NOT NULL,
    "next_attempt_at"  TEXT,
    "reason"           TEXT,
    "sent_at"          TEXT,
    "updated_at"       TEXT NOT NULL
);

-- Finding leases that lapsed.
CREATE INDEX IF NOT EXISTS "notify_email_deliveries_lease_idx"
    ON "notify_email_deliveries" ("status", "lease_until");

-- Finding retries that are due.
CREATE INDEX IF NOT EXISTS "notify_email_deliveries_retry_idx"
    ON "notify_email_deliveries" ("status", "next_attempt_at");
