package sqlstore

import (
	"context"
	"embed"
	"regexp"
	"strings"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// The tables the store owns, before any prefix.
const (
	// NotificationsTable holds one row per notification.
	NotificationsTable = "notify_notifications"
	// WatermarksTable holds each subject's close records.
	WatermarksTable = "notify_watermarks"
)

// documents holds the published DDL, one document per dialect.
//
//go:embed ddl/*.sql
var documents embed.FS

// documentFiles names each dialect's DDL document.
var documentFiles = map[string]string{
	sqlkit.PostgreSQL.Name(): "ddl/postgres.sql",
	sqlkit.MySQL.Name():      "ddl/mysql.sql",
	sqlkit.SQLite.Name():     "ddl/sqlite.sql",
}

// Store is a [notify.Store] over a [sqlkit.Executor].
//
// A Store is safe for concurrent use, and so is using several Stores, in one
// process or many, over the same tables.
type Store struct {
	executor sqlkit.Executor
	execer   sqlkit.Execer
	querier  sqlkit.Querier
	dialect  sqlkit.Dialect
	prefix   string
	document string
}

var _ notify.Store = (*Store)(nil)

// Option configures a [Store].
type Option func(*config)

// config is what the options set.
type config struct {
	prefix string
}

// WithTablePrefix prefixes every table and index name the store owns, so that
// it can share a database whose names would otherwise collide. The default is
// no prefix. A prefix may contain only letters, digits and underscores.
func WithTablePrefix(prefix string) Option {
	return func(c *config) { c.prefix = prefix }
}

// plainPrefix is what a table prefix may contain. The prefix is written into
// DDL documents verbatim, so anything that could close a quoted identifier is
// refused rather than escaped.
var plainPrefix = regexp.MustCompile(`^[A-Za-z0-9_]*$`)

// New builds a store over an executor, in the executor's dialect.
//
// The executor must also run schema statements, as every sqlkit executor module
// does, and its dialect must be one this store publishes a schema for:
// PostgreSQL, MySQL or SQLite. A nil executor, an executor that fails either
// requirement, or a prefix that is not a plain identifier is a
// [notify.ConfigurationError].
func New(executor sqlkit.Executor, opts ...Option) (*Store, error) {
	if executor == nil {
		return nil, &notify.ConfigurationError{Detail: "sqlstore: an executor is required"}
	}

	var cfg config

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	execer, isExecer := executor.(sqlkit.Execer)
	querier, isQuerier := executor.(sqlkit.Querier)
	dialect := executor.Dialect()

	switch {
	case !isExecer || !isQuerier:
		return nil, &notify.ConfigurationError{
			Detail: "sqlstore: the executor must also implement sqlkit.Execer and sqlkit.Querier, " +
				"as every sqlkit executor module does",
		}
	case dialect == nil:
		return nil, &notify.ConfigurationError{Detail: "sqlstore: the executor has no dialect"}
	case !plainPrefix.MatchString(cfg.prefix):
		return nil, &notify.ConfigurationError{
			Detail: "sqlstore: a table prefix may contain only letters, digits and underscores",
		}
	}

	document, err := documentFor(dialect)
	if err != nil {
		return nil, err
	}

	return &Store{
		executor: executor,
		execer:   execer,
		querier:  querier,
		dialect:  dialect,
		prefix:   cfg.prefix,
		document: document,
	}, nil
}

// documentFor reads a dialect's DDL document.
func documentFor(dialect sqlkit.Dialect) (string, error) {
	file, ok := documentFiles[dialect.Name()]
	if !ok {
		return "", &notify.ConfigurationError{
			Detail: "sqlstore: no schema is published for the " + dialect.Name() +
				" dialect; PostgreSQL, MySQL and SQLite are supported",
		}
	}

	raw, err := documents.ReadFile(file)
	if err != nil {
		return "", &notify.ConfigurationError{Detail: "sqlstore: read the " + dialect.Name() + " schema: " + err.Error()}
	}

	return string(raw), nil
}

// Dialect returns the dialect the store writes SQL for.
func (s *Store) Dialect() sqlkit.Dialect { return s.dialect }

// Tables returns the store's tables, prefixed, in the order they are created.
func (s *Store) Tables() []string {
	return []string{s.prefix + NotificationsTable, s.prefix + WatermarksTable}
}

// Schema returns the store's DDL for its dialect, with the table prefix
// applied: the document a host applies through its own migration pipeline.
func (s *Store) Schema() string {
	return strings.ReplaceAll(s.document, sqlkit.PrefixToken, s.prefix)
}

// Migrate applies the schema. It exists for tests and development: a host
// applies [Store.Schema] through its own migration pipeline, and nothing in
// normal operation changes the schema on its own. Every statement is IF NOT
// EXISTS, so migrating an existing schema changes nothing.
func (s *Store) Migrate(ctx context.Context) error {
	return sqlkit.ApplySchema(ctx, s.execer, s.dialect, sqlkit.RenderSchema(s.document, s.prefix))
}

// schemaExpectation is what [Store.VerifySchema] requires, before the table
// prefix. Indexes are required by name: a missing one does not make a statement
// fail, it makes it read the whole table, which is found in production rather
// than at startup unless something looks for it.
var schemaExpectation = sqlkit.SchemaExpectation{
	NotificationsTable: {
		Columns:           notificationColumns,
		IdentifierColumns: []string{"id", "recipient", "source_id", "subject", "kind", "state"},
		Indexes: []string{
			"notify_notifications_source_key",
			"notify_notifications_recipient_idx",
			"notify_notifications_state_idx",
			"notify_notifications_subject_idx",
			"notify_notifications_inactive_idx",
		},
	},
	WatermarksTable: {
		Columns:           []string{"subject", "kind", "version", "updated_at"},
		IdentifierColumns: []string{"subject", "kind"},
		Indexes:           []string{"notify_watermarks_updated_idx"},
	},
}

// VerifySchema compares the live database with what the store's statements
// require: both tables, every column, the collation of every identifier column,
// and every index. It reports every discrepancy at once as a
// [*sqlkit.SchemaError], which matches [sqlkit.ErrSchemaMismatch], rather than
// failing on first use. Call it at startup.
func (s *Store) VerifySchema(ctx context.Context) error {
	return sqlkit.VerifySchema(s.own(ctx), s.querier, s.dialect, s.prefix, schemaExpectation)
}
