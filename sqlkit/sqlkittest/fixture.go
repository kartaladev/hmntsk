package sqlkittest

import "github.com/kartaladev/hmntsk/sqlkit"

// The fixture is a schema with no domain: two tables, identifier columns that
// must compare case-sensitively, one secondary index, one JSON payload column,
// one timestamp column and a foreign key. It is the least a store needs, and
// enough to prove an executor, rendering, the development runner and
// verification behave the same on every combination.

// FixtureTables are the fixture's tables, unprefixed, in creation order.
var FixtureTables = []string{"widgets", "parts"}

// FixtureExpectation is what the fixture requires of the live schema.
var FixtureExpectation = sqlkit.SchemaExpectation{
	"widgets": {
		Columns:           []string{"id", "owner", "payload", "at", "score"},
		IdentifierColumns: []string{"id", "owner"},
		Indexes:           []string{"widgets_owner_idx"},
	},
	"parts": {
		Columns:           []string{"id", "widget_id"},
		IdentifierColumns: []string{"id", "widget_id"},
	},
}

// FixtureSchema returns the fixture's schema document for a dialect, carrying
// [sqlkit.PrefixToken] wherever a table prefix goes. It reports false for a
// dialect it has no document for.
func FixtureSchema(dialect sqlkit.Dialect) (string, bool) {
	document, ok := fixtureSchemas[dialect.Name()]

	return document, ok
}

// fixtureSchemas are the fixture documents, by dialect name.
var fixtureSchemas = map[string]string{
	"postgres": `-- sqlkit conformance fixture, PostgreSQL.
CREATE TABLE IF NOT EXISTS "{{PREFIX}}widgets" (
    "id"      text COLLATE "C" NOT NULL,
    "owner"   text COLLATE "C" NOT NULL,
    "payload" text,
    "at"      timestamptz(6),
    "score"   bigint NOT NULL DEFAULT 0,
    PRIMARY KEY ("id")
);
CREATE INDEX IF NOT EXISTS "{{PREFIX}}widgets_owner_idx" ON "{{PREFIX}}widgets" ("owner");
CREATE TABLE IF NOT EXISTS "{{PREFIX}}parts" (
    "id"        text COLLATE "C" NOT NULL,
    "widget_id" text COLLATE "C" NOT NULL REFERENCES "{{PREFIX}}widgets" ("id"),
    PRIMARY KEY ("id")
);
`,
	"mysql": "-- sqlkit conformance fixture, MySQL 8.0 or later.\n" +
		"CREATE TABLE IF NOT EXISTS `{{PREFIX}}widgets` (\n" +
		"    `id`      VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL,\n" +
		"    `owner`   VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,\n" +
		"    `payload` LONGTEXT NULL,\n" +
		"    `at`      DATETIME(6) NULL,\n" +
		"    `score`   BIGINT NOT NULL DEFAULT 0,\n" +
		"    PRIMARY KEY (`id`),\n" +
		"    KEY `{{PREFIX}}widgets_owner_idx` (`owner`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
		"CREATE TABLE IF NOT EXISTS `{{PREFIX}}parts` (\n" +
		"    `id`        VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,\n" +
		"    `widget_id` VARCHAR(64) COLLATE utf8mb4_0900_as_cs NOT NULL,\n" +
		"    PRIMARY KEY (`id`),\n" +
		"    CONSTRAINT `{{PREFIX}}parts_widget_fk` FOREIGN KEY (`widget_id`) REFERENCES `{{PREFIX}}widgets` (`id`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n",
	"sqlite": `-- sqlkit conformance fixture, SQLite 3.35 or later.
CREATE TABLE IF NOT EXISTS "{{PREFIX}}widgets" (
    "id"      TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "owner"   TEXT COLLATE BINARY NOT NULL,
    "payload" TEXT,
    "at"      TEXT,
    "score"   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS "{{PREFIX}}widgets_owner_idx" ON "{{PREFIX}}widgets" ("owner");
CREATE TABLE IF NOT EXISTS "{{PREFIX}}parts" (
    "id"        TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "widget_id" TEXT COLLATE BINARY NOT NULL REFERENCES "{{PREFIX}}widgets" ("id")
);
`,
}

// brokenSchemas are fixture documents with three deliberate discrepancies: no
// parts table, no score column and no owner index.
var brokenSchemas = map[string]string{
	"postgres": `CREATE TABLE IF NOT EXISTS "{{PREFIX}}widgets" (
    "id"      text COLLATE "C" NOT NULL PRIMARY KEY,
    "owner"   text COLLATE "C" NOT NULL,
    "payload" text,
    "at"      timestamptz(6)
);
`,
	"mysql": "CREATE TABLE IF NOT EXISTS `{{PREFIX}}widgets` (\n" +
		"    `id`      VARCHAR(64)  COLLATE utf8mb4_0900_as_cs NOT NULL PRIMARY KEY,\n" +
		"    `owner`   VARCHAR(255) COLLATE utf8mb4_0900_as_cs NOT NULL,\n" +
		"    `payload` LONGTEXT NULL,\n" +
		"    `at`      DATETIME(6) NULL\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n",
	"sqlite": `CREATE TABLE IF NOT EXISTS "{{PREFIX}}widgets" (
    "id"      TEXT COLLATE BINARY NOT NULL PRIMARY KEY,
    "owner"   TEXT COLLATE BINARY NOT NULL,
    "payload" TEXT,
    "at"      TEXT
);
`,
}
