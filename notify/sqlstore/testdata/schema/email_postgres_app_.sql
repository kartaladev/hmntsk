-- PostgreSQL schema for notify's optional email delivery records.
--
-- A host that emails notifications applies this document in addition to the
-- notification store's own; a host that does not never needs it.
--
-- One row per notification that a dispatcher has considered. There is no
-- foreign key to the notifications table: retention deletes notifications in
-- batches on every dialect, and the dispatcher removes the records it leaves
-- behind.
--
-- Identifier columns are pinned to the C collation, as in the notification
-- store's schema.

CREATE TABLE IF NOT EXISTS "app_notify_email_deliveries" (
    "notification_id"  text COLLATE "C" NOT NULL,
    "recipient"        text COLLATE "C" NOT NULL,
    "status"           text COLLATE "C" NOT NULL,
    "batch_id"         text COLLATE "C",
    "owner"            text COLLATE "C",
    "lease_until"      timestamptz(6),
    "attempts"         integer NOT NULL,
    "next_attempt_at"  timestamptz(6),
    "reason"           text,
    "sent_at"          timestamptz(6),
    "updated_at"       timestamptz(6) NOT NULL,
    CONSTRAINT "app_notify_email_deliveries_pkey" PRIMARY KEY ("notification_id")
);

-- Finding leases that lapsed.
CREATE INDEX IF NOT EXISTS "app_notify_email_deliveries_lease_idx"
    ON "app_notify_email_deliveries" ("status", "lease_until");

-- Finding retries that are due.
CREATE INDEX IF NOT EXISTS "app_notify_email_deliveries_retry_idx"
    ON "app_notify_email_deliveries" ("status", "next_attempt_at");
