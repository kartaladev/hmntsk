package sqlcore

import (
	"context"
	"embed"
	"fmt"
	"strings"
)

// ddlFiles holds the per-dialect schema. It ships with the module so that hosts
// feed it to whatever migration tool they already run, rather than each adopter
// reinventing a schema the conformance suite has to build on all three engines
// anyway.
//
//go:embed ddl/*.sql
var ddlFiles embed.FS

// prefixToken is what the embedded DDL carries where the host's table prefix
// goes.
const prefixToken = "{{PREFIX}}"

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
	if dialect == nil {
		return nil, fmt.Errorf("sqlcore: a dialect is required to render migrations")
	}

	raw, err := ddlFiles.ReadFile("ddl/" + dialect.Name() + ".sql")
	if err != nil {
		return nil, fmt.Errorf("sqlcore: no schema is published for dialect %q: %w", dialect.Name(), err)
	}

	return splitStatements(strings.ReplaceAll(string(raw), prefixToken, prefix)), nil
}

// MigrationsSource returns a dialect's schema as one document, prefix applied.
// It is what a host pastes into a migration file.
func (b *Builder) MigrationsSource() (string, error) {
	raw, err := ddlFiles.ReadFile("ddl/" + b.dialect.Name() + ".sql")
	if err != nil {
		return "", fmt.Errorf("sqlcore: no schema is published for dialect %q: %w", b.dialect.Name(), err)
	}

	return strings.ReplaceAll(string(raw), prefixToken, b.prefix), nil
}

// splitStatements breaks a DDL document into executable statements, dropping
// comments and blank lines. The DDL is written so that a statement ends at a
// semicolon on the end of a line, which keeps this honest without a parser.
func splitStatements(document string) []string {
	var (
		statements []string
		current    strings.Builder
	)

	for line := range strings.Lines(document) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		current.WriteString(trimmed)
		current.WriteString("\n")

		if strings.HasSuffix(trimmed, ";") {
			statements = append(statements, strings.TrimSuffix(strings.TrimSpace(current.String()), ";"))
			current.Reset()
		}
	}

	if remainder := strings.TrimSpace(current.String()); remainder != "" {
		statements = append(statements, remainder)
	}

	return statements
}

// Execer is the little of a connection the development migration runner needs.
type Execer interface {
	// ExecStatement runs a statement that returns no rows.
	ExecStatement(ctx context.Context, sql string, args ...any) error
}

// Querier is the little of a connection schema verification needs.
type Querier interface {
	// QueryStatement runs a statement and returns its rows.
	QueryStatement(ctx context.Context, sql string, args ...any) (Rows, error)
}

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

	for _, statement := range statements {
		if err := execer.ExecStatement(ctx, statement); err != nil {
			return fmt.Errorf("sqlcore: apply schema on %s: %w\nstatement: %s",
				b.dialect.Name(), err, statement)
		}
	}

	return nil
}

// Drop removes the engine's tables. It exists for tests, so that one database
// can serve several cases, and is deliberately not part of the published
// migration path.
func (b *Builder) Drop(ctx context.Context, execer Execer) error {
	tables := b.Tables()

	for i := len(tables) - 1; i >= 0; i-- {
		statement := "DROP TABLE IF EXISTS " + b.dialect.Quote(tables[i])
		if b.dialect.Name() == "postgres" {
			statement += " CASCADE"
		}

		if err := execer.ExecStatement(ctx, statement); err != nil {
			return fmt.Errorf("sqlcore: drop %s: %w", tables[i], err)
		}
	}

	return nil
}
