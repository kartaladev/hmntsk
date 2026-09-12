-- SQLite schema for the hmntsk engine, 3.35 or later.
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
-- Table names below carry the host's configured prefix, applied when this file
-- is read; with no prefix configured they are exactly as written.

CREATE TABLE IF NOT EXISTS "{{PREFIX}}tasks" (
    "id"                 TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "task_type"          TEXT COLLATE BINARY NOT NULL,
    "version"            INTEGER NOT NULL,
    "status"             TEXT COLLATE BINARY NOT NULL,
    "suspended_from"     TEXT COLLATE BINARY,
    "priority"           INTEGER NOT NULL,
    "assignee"           TEXT COLLATE BINARY,
    "owner_type"         TEXT COLLATE BINARY,
    "owner_ref"          TEXT COLLATE BINARY,
    "activity_key"       TEXT COLLATE BINARY,
    "correlation_extra"  TEXT,
    "callback_address"   TEXT,
    "callback_params"    TEXT,
    "escalation"         TEXT,
    "input"              TEXT,
    "progress"           TEXT,
    "output"             TEXT,
    "reason"             TEXT,
    "created_by"         TEXT COLLATE BINARY,
    "escalation_count"   INTEGER NOT NULL DEFAULT 0,
    "created_at"         TEXT NOT NULL,
    "updated_at"         TEXT NOT NULL,
    "due_at"             TEXT,
    "started_at"         TEXT,
    "closed_at"          TEXT,
    "escalated_at"       TEXT,
    "locked_by"          TEXT COLLATE BINARY,
    "locked_until"       TEXT
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_candidates" (
    "task_id"  TEXT COLLATE BINARY NOT NULL,
    "kind"     TEXT COLLATE BINARY NOT NULL,
    "value"    TEXT COLLATE BINARY NOT NULL,
    "ordinal"  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY ("task_id", "kind", "value"),
    FOREIGN KEY ("task_id") REFERENCES "{{PREFIX}}tasks" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_history" (
    "task_id"      TEXT COLLATE BINARY NOT NULL,
    "version"      INTEGER NOT NULL,
    "operation"    TEXT COLLATE BINARY NOT NULL,
    "from_status"  TEXT COLLATE BINARY NOT NULL,
    "to_status"    TEXT COLLATE BINARY NOT NULL,
    "actor"        TEXT COLLATE BINARY,
    "comment"      TEXT,
    "at"           TEXT NOT NULL,
    PRIMARY KEY ("task_id", "version"),
    FOREIGN KEY ("task_id") REFERENCES "{{PREFIX}}tasks" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_outbox" (
    "id"            TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "task_id"       TEXT COLLATE BINARY NOT NULL,
    "task_type"     TEXT COLLATE BINARY NOT NULL,
    "event_type"    TEXT COLLATE BINARY NOT NULL,
    "occurred_at"   TEXT NOT NULL,
    "published_at"  TEXT,
    "payload"       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_types" (
    "name"                TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "title"               TEXT,
    "description"         TEXT,
    "input_schema"        TEXT,
    "output_schema"       TEXT,
    "default_priority"    INTEGER NOT NULL DEFAULT 5,
    "default_deadline_ms" INTEGER NOT NULL DEFAULT 0,
    "default_escalation"  TEXT,
    "default_assignment"  TEXT,
    "updated_at"          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_assignee_idx" ON "{{PREFIX}}tasks" ("assignee", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_status_idx" ON "{{PREFIX}}tasks" ("status", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_type_idx" ON "{{PREFIX}}tasks" ("task_type", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_correlation_idx" ON "{{PREFIX}}tasks" ("owner_type", "owner_ref", "activity_key");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_due_idx" ON "{{PREFIX}}tasks" ("due_at", "status");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}task_candidates_lookup_idx" ON "{{PREFIX}}task_candidates" ("kind", "value", "task_id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}task_outbox_unpublished_idx" ON "{{PREFIX}}task_outbox" ("published_at", "occurred_at", "id");
