package sqlcore

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// ddlFiles holds the per-dialect schema. It ships with the module so that hosts
// feed it to whatever migration tool they already run, rather than each adopter
// reinventing a schema the conformance suite has to build on all three engines
// anyway.
//
//go:embed ddl/*.sql
var ddlFiles embed.FS

// Migrations returns the statements that create the engine's schema on a
// dialect, with the builder's table prefix applied, in the order they must run.
//
// The engine never applies these itself in normal operation. Running DDL at
// startup is forbidden outright in many organisations, and choosing a migration
// tool on an adopter's behalf is not this library's business: it publishes the
// schema and verifies the live one, and the host owns the pipeline in between.
func (b *Builder) Migrations() ([]string, error) {
	return migrationsFor(b.dialect, b.prefix)
}

// Migrations returns the schema statements for a dialect with no table prefix.
func Migrations(dialect Dialect) ([]string, error) {
	return migrationsFor(dialect, "")
}

// migrationsFor reads and renders a dialect's DDL.
func migrationsFor(dialect Dialect, prefix string) ([]string, error) {
	raw, err := schemaDocument(dialect)
	if err != nil {
		return nil, err
	}

	return sqlkit.RenderSchema(raw, prefix), nil
}

// MigrationsSource returns a dialect's schema as one document, prefix applied.
// It is what a host pastes into a migration file.
func (b *Builder) MigrationsSource() (string, error) {
	raw, err := schemaDocument(b.dialect)
	if err != nil {
		return "", err
	}

	return strings.ReplaceAll(raw, sqlkit.PrefixToken, b.prefix), nil
}

// schemaDocument reads a dialect's published DDL, prefix token and comments
// included.
func schemaDocument(dialect Dialect) (string, error) {
	if dialect == nil {
		return "", fmt.Errorf("sqlcore: a dialect is required to render migrations")
	}

	raw, err := ddlFiles.ReadFile("ddl/" + dialect.Name() + ".sql")
	if err != nil {
		return "", fmt.Errorf("sqlcore: no schema is published for dialect %q: %w", dialect.Name(), err)
	}

	return string(raw), nil
}

// Execer is the little of a connection the development migration runner needs.
// It is [sqlkit.Execer].
type Execer = sqlkit.Execer

// Querier is the little of a connection schema verification needs. It is
// [sqlkit.Querier].
type Querier = sqlkit.Querier

// Migrate applies the schema.
//
// It exists for tests and for development, where the conformance suite has to
// build these schemas on three engines on every run. It is not a migration
// tool: it has no versioning, no down direction and no locking, and a
// production deployment should never call it.
func (b *Builder) Migrate(ctx context.Context, execer Execer) error {
	statements, err := b.Migrations()
	if err != nil {
		return err
	}

	return sqlkit.ApplySchema(ctx, execer, b.dialect, statements)
}

// Drop removes the engine's tables. It exists for tests, so that one database
// can serve several cases, and is deliberately not part of the published
// migration path.
func (b *Builder) Drop(ctx context.Context, execer Execer) error {
	return sqlkit.DropTables(ctx, execer, b.dialect, b.Tables())
}
