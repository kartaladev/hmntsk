package sqlkit

import (
	"context"
	"fmt"
	"strings"
)

// PrefixToken is what a published schema document carries wherever the host's
// table prefix goes: in table names, index names and foreign-key references
// alike.
const PrefixToken = "{{PREFIX}}"

// RenderSchema substitutes a table prefix into a schema document and splits it
// into executable statements, in document order. An empty prefix leaves the
// names bare and no token behind.
func RenderSchema(document, prefix string) []string {
	return SplitStatements(strings.ReplaceAll(document, PrefixToken, prefix))
}

// SplitStatements breaks a schema document into executable statements, dropping
// comments and blank lines.
//
// A statement ends at a semicolon on the end of a line, which a published
// schema is written to respect; that keeps this honest without a SQL parser. A
// trailing statement with no semicolon is kept.
func SplitStatements(document string) []string {
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

// ApplySchema runs schema statements in order, stopping at the first failure
// and naming the statement that failed.
//
// It exists for tests and for development, where a conformance suite has to
// build a schema on three engines on every run. It is not a migration tool: it
// has no versioning, no down direction and no locking, and a production
// deployment applies the published schema through its own pipeline instead.
func ApplySchema(ctx context.Context, execer Execer, dialect Dialect, statements []string) error {
	for _, statement := range statements {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := execer.ExecStatement(ctx, statement); err != nil {
			return fmt.Errorf("sqlkit: apply schema on %s: %w\nstatement: %s", dialect.Name(), err, statement)
		}
	}

	return nil
}

// DropTables removes tables in reverse of the order given, so that a table is
// dropped after everything created later that may reference it. On PostgreSQL
// each drop cascades.
//
// Like [ApplySchema], it exists for tests, so that one database can serve many
// cases, and is not part of any published migration path.
func DropTables(ctx context.Context, execer Execer, dialect Dialect, tables []string) error {
	for i := len(tables) - 1; i >= 0; i-- {
		statement := "DROP TABLE IF EXISTS " + dialect.Quote(tables[i])
		if dialect.Name() == PostgreSQL.Name() {
			statement += " CASCADE"
		}

		if err := execer.ExecStatement(ctx, statement); err != nil {
			return fmt.Errorf("sqlkit: drop %s: %w", tables[i], err)
		}
	}

	return nil
}
