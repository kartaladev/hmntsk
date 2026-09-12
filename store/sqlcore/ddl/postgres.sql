-- PostgreSQL schema for the hmntsk engine.
--
-- Every identifier column is pinned to the C collation. PostgreSQL's default
-- collation compares case-sensitively, so that is not what this is for: it is
-- for ordering. A locale-aware collation can sort 'a-b' before 'ab', and task
-- identifiers contain hyphens, which would make keyset pagination skip or
-- repeat rows. C collation is byte order, which is the order the identifiers
-- were minted in.
--
-- {{PREFIX}} is replaced with the host's configured table prefix.

CREATE TABLE IF NOT EXISTS "{{PREFIX}}tasks" (
    "id"                 text COLLATE "C" NOT NULL,
    "task_type"          text COLLATE "C" NOT NULL,
    "version"            bigint NOT NULL,
    "status"             text COLLATE "C" NOT NULL,
    "suspended_from"     text COLLATE "C",
    "priority"           integer NOT NULL,
    "assignee"           text COLLATE "C",
    "owner_type"         text COLLATE "C",
    "owner_ref"          text COLLATE "C",
    "activity_key"       text COLLATE "C",
    "correlation_extra"  jsonb,
    "callback_address"   text,
    "callback_params"    jsonb,
    "escalation"         jsonb,
    "input"              jsonb,
    "progress"           jsonb,
    "output"             jsonb,
    "reason"             text,
    "created_by"         text COLLATE "C",
    "escalation_count"   integer NOT NULL DEFAULT 0,
    "created_at"         timestamptz(6) NOT NULL,
    "updated_at"         timestamptz(6) NOT NULL,
    "due_at"             timestamptz(6),
    "started_at"         timestamptz(6),
    "closed_at"          timestamptz(6),
    "escalated_at"       timestamptz(6),
    "locked_by"          text COLLATE "C",
    "locked_until"       timestamptz(6),
    CONSTRAINT "{{PREFIX}}tasks_pkey" PRIMARY KEY ("id")
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_candidates" (
    "task_id"  text COLLATE "C" NOT NULL,
    "kind"     text COLLATE "C" NOT NULL,
    "value"    text COLLATE "C" NOT NULL,
    "ordinal"  integer NOT NULL DEFAULT 0,
    CONSTRAINT "{{PREFIX}}task_candidates_pkey" PRIMARY KEY ("task_id", "kind", "value"),
    CONSTRAINT "{{PREFIX}}task_candidates_task_fk" FOREIGN KEY ("task_id")
        REFERENCES "{{PREFIX}}tasks" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_history" (
    "task_id"      text COLLATE "C" NOT NULL,
    "version"      bigint NOT NULL,
    "operation"    text COLLATE "C" NOT NULL,
    "from_status"  text COLLATE "C" NOT NULL,
    "to_status"    text COLLATE "C" NOT NULL,
    "actor"        text COLLATE "C",
    "comment"      text,
    "at"           timestamptz(6) NOT NULL,
    CONSTRAINT "{{PREFIX}}task_history_pkey" PRIMARY KEY ("task_id", "version"),
    CONSTRAINT "{{PREFIX}}task_history_task_fk" FOREIGN KEY ("task_id")
        REFERENCES "{{PREFIX}}tasks" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_outbox" (
    "id"            text COLLATE "C" NOT NULL,
    "task_id"       text COLLATE "C" NOT NULL,
    "task_type"     text COLLATE "C" NOT NULL,
    "event_type"    text COLLATE "C" NOT NULL,
    "occurred_at"   timestamptz(6) NOT NULL,
    "published_at"  timestamptz(6),
    "payload"       jsonb NOT NULL,
    CONSTRAINT "{{PREFIX}}task_outbox_pkey" PRIMARY KEY ("id")
);

CREATE TABLE IF NOT EXISTS "{{PREFIX}}task_types" (
    "name"                text COLLATE "C" NOT NULL,
    "title"               text,
    "description"         text,
    "input_schema"        jsonb,
    "output_schema"       jsonb,
    "default_priority"    integer NOT NULL DEFAULT 5,
    "default_deadline_ms" bigint NOT NULL DEFAULT 0,
    "default_escalation"  jsonb,
    "default_assignment"  jsonb,
    "updated_at"          timestamptz(6) NOT NULL,
    CONSTRAINT "{{PREFIX}}task_types_pkey" PRIMARY KEY ("name")
);

CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_assignee_idx" ON "{{PREFIX}}tasks" ("assignee", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_status_idx" ON "{{PREFIX}}tasks" ("status", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_type_idx" ON "{{PREFIX}}tasks" ("task_type", "id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_correlation_idx" ON "{{PREFIX}}tasks" ("owner_type", "owner_ref", "activity_key");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}tasks_due_idx" ON "{{PREFIX}}tasks" ("due_at", "status");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}task_candidates_lookup_idx" ON "{{PREFIX}}task_candidates" ("kind", "value", "task_id");
CREATE INDEX IF NOT EXISTS "{{PREFIX}}task_outbox_unpublished_idx" ON "{{PREFIX}}task_outbox" ("published_at", "occurred_at", "id");
