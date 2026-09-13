-- SQLite schema for the notify notification store, 3.35 or later.
--
-- SQLite has no native timestamp type, so instants are stored as TEXT in one
-- fixed encoding: UTC, RFC 3339, exactly six fractional digits. That makes
-- lexical ordering equal chronological ordering, and makes a value read back
-- compare equal to the value written.
--
-- BINARY is SQLite's default collation and is already case-sensitive. It is
-- written out anyway so that the three schemas say the same thing in the same
-- place, and so that schema verification has something to check.
--
-- Table and index names carry the host's configured prefix, applied when this
-- document is rendered; with no prefix configured they are exactly as written.

CREATE TABLE IF NOT EXISTS "{{PREFIX}}notify_notifications" (
    "id"               TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "recipient"        TEXT COLLATE BINARY NOT NULL,
    "source_id"        TEXT COLLATE BINARY NOT NULL,
    "subject"          TEXT COLLATE BINARY NOT NULL,
    "subject_version"  INTEGER NOT NULL,
    "kind"             TEXT COLLATE BINARY NOT NULL,
    "state"            TEXT COLLATE BINARY NOT NULL,
    "closed_reason"    TEXT,
    "title"            TEXT,
    "links"            TEXT,
    "data"             TEXT,
    "created_at"       TEXT NOT NULL,
    "read_at"          TEXT,
    "closed_at"        TEXT,
    "inactive_at"      TEXT
);

-- Idempotency: one notification per source and recipient.
CREATE UNIQUE INDEX IF NOT EXISTS "{{PREFIX}}notify_notifications_source_key"
    ON "{{PREFIX}}notify_notifications" ("source_id", "recipient");

-- A recipient's listing, newest first, and its keyset paging.
CREATE INDEX IF NOT EXISTS "{{PREFIX}}notify_notifications_recipient_idx"
    ON "{{PREFIX}}notify_notifications" ("recipient", "created_at", "id");

-- Counting active notifications, and choosing what the count bound deletes.
CREATE INDEX IF NOT EXISTS "{{PREFIX}}notify_notifications_state_idx"
    ON "{{PREFIX}}notify_notifications" ("recipient", "state");

-- Closing a subject's notifications by kind, and coalescing.
CREATE INDEX IF NOT EXISTS "{{PREFIX}}notify_notifications_subject_idx"
    ON "{{PREFIX}}notify_notifications" ("subject", "kind", "state");

-- The age bound.
CREATE INDEX IF NOT EXISTS "{{PREFIX}}notify_notifications_inactive_idx"
    ON "{{PREFIX}}notify_notifications" ("state", "inactive_at");

-- A subject's close records: the highest version closed per kind, and per
-- every kind under kind '*'. SQLite has one writer, which is what serialises
-- publishing and closing a subject.
CREATE TABLE IF NOT EXISTS "{{PREFIX}}notify_watermarks" (
    "subject"     TEXT COLLATE BINARY NOT NULL,
    "kind"        TEXT COLLATE BINARY NOT NULL,
    "version"     INTEGER NOT NULL,
    "updated_at"  TEXT NOT NULL,
    PRIMARY KEY ("subject", "kind")
);

-- Expiring close records.
CREATE INDEX IF NOT EXISTS "{{PREFIX}}notify_watermarks_updated_idx"
    ON "{{PREFIX}}notify_watermarks" ("updated_at");
