-- PostgreSQL schema for the notify notification store.
--
-- Identifier columns are pinned to the C collation. The default collation
-- already compares case-sensitively; C is for ordering. A locale-aware
-- collation can sort 'a-b' before 'ab', and notification identifiers contain
-- hyphens, which would make keyset paging skip or repeat rows. C is byte order,
-- which is the order the identifiers were minted in.
--
-- links and data are text, not jsonb. jsonb reorders object keys and rewrites
-- number literals, and the store promises to return a publisher's payload
-- exactly as it was published.
--
-- Table and index names carry the host's configured prefix, applied when this
-- document is rendered; with no prefix configured they are exactly as written.

CREATE TABLE IF NOT EXISTS "app_notify_notifications" (
    "id"               text COLLATE "C" NOT NULL,
    "recipient"        text COLLATE "C" NOT NULL,
    "source_id"        text COLLATE "C" NOT NULL,
    "subject"          text COLLATE "C" NOT NULL,
    "subject_version"  bigint NOT NULL,
    "kind"             text COLLATE "C" NOT NULL,
    "state"            text COLLATE "C" NOT NULL,
    "closed_reason"    text,
    "title"            text,
    "links"            text,
    "data"             text,
    "created_at"       timestamptz(6) NOT NULL,
    "read_at"          timestamptz(6),
    "closed_at"        timestamptz(6),
    "inactive_at"      timestamptz(6),
    CONSTRAINT "app_notify_notifications_pkey" PRIMARY KEY ("id")
);

-- Idempotency: one notification per source and recipient.
CREATE UNIQUE INDEX IF NOT EXISTS "app_notify_notifications_source_key"
    ON "app_notify_notifications" ("source_id", "recipient");

-- A recipient's listing, newest first, and its keyset paging.
CREATE INDEX IF NOT EXISTS "app_notify_notifications_recipient_idx"
    ON "app_notify_notifications" ("recipient", "created_at", "id");

-- Counting active notifications, and choosing what the count bound deletes.
CREATE INDEX IF NOT EXISTS "app_notify_notifications_state_idx"
    ON "app_notify_notifications" ("recipient", "state");

-- Closing a subject's notifications by kind, and coalescing.
CREATE INDEX IF NOT EXISTS "app_notify_notifications_subject_idx"
    ON "app_notify_notifications" ("subject", "kind", "state");

-- The age bound.
CREATE INDEX IF NOT EXISTS "app_notify_notifications_inactive_idx"
    ON "app_notify_notifications" ("state", "inactive_at");

-- A subject's close records: the highest version closed per kind, and per
-- every kind under kind '*'. Publishing and closing a subject both lock its '*'
-- row first, which is what serialises them.
CREATE TABLE IF NOT EXISTS "app_notify_watermarks" (
    "subject"     text COLLATE "C" NOT NULL,
    "kind"        text COLLATE "C" NOT NULL,
    "version"     bigint NOT NULL,
    "updated_at"  timestamptz(6) NOT NULL,
    CONSTRAINT "app_notify_watermarks_pkey" PRIMARY KEY ("subject", "kind")
);

-- Expiring close records.
CREATE INDEX IF NOT EXISTS "app_notify_watermarks_updated_idx"
    ON "app_notify_watermarks" ("updated_at");
