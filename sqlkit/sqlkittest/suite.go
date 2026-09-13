package sqlkittest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// Harness is what the executor suite needs from one executor on one database.
type Harness struct {
	// Executor is the executor under test. It must also implement
	// [sqlkit.Execer] and [sqlkit.Querier].
	Executor sqlkit.Executor
	// CallerTx begins a transaction the way a host's own code would, and
	// returns a context carrying it — through the executor package's
	// ContextWithTx — plus a function that commits or rolls it back. It is how
	// the suite checks that an executor joins a transaction it did not begin
	// and leaves its disposition alone.
	CallerTx func(ctx context.Context) (scoped context.Context, done func(commit bool) error)
}

// Factory builds a harness. It is called once per case and may register
// cleanup with t. The suite creates and drops its own prefixed fixture schema
// in the harness's database, so one database serves every case.
type Factory func(t *testing.T) Harness

// errInjected is the failure the suite's scopes return.
var errInjected = errors.New("sqlkittest: injected failure")

// RunExecutorSuite proves that an executor behaves like every other: how it
// runs statements and releases rows, who begins and who commits, what nesting
// means, what a failure leaves behind, and that values survive the round trip
// identically on every driver and dialect.
func RunExecutorSuite(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("statements", func(t *testing.T) { runStatementCases(t, factory) })
	t.Run("transactions", func(t *testing.T) { runTransactionCases(t, factory) })
	t.Run("rollback", func(t *testing.T) { runRollbackCases(t, factory) })
	t.Run("values", func(t *testing.T) { runValueCases(t, factory) })
	t.Run("schema", func(t *testing.T) { runSchemaCases(t, factory) })
}

// fixture is one case's harness over its own freshly applied fixture schema.
type fixture struct {
	Harness

	prefix string
}

// prefixes hands out a distinct table prefix per fixture.
var prefixes atomic.Uint64

// nextPrefix returns a table prefix no other fixture in this test binary uses.
func nextPrefix() string { return fmt.Sprintf("sqlkit%d_", prefixes.Add(1)) }

// newFixture builds a harness and applies a schema document under a fresh
// prefix, dropping it when the case ends.
func newFixture(t *testing.T, factory Factory, documents map[string]string) fixture {
	t.Helper()

	h := factory(t)
	require.NotNil(t, h.Executor, "the harness must supply an executor")

	execer, ok := h.Executor.(sqlkit.Execer)
	require.True(t, ok, "every executor implements sqlkit.Execer, so the schema runner can drive it")

	_, ok = h.Executor.(sqlkit.Querier)
	require.True(t, ok, "every executor implements sqlkit.Querier, so verification can drive it")

	dialect := h.Executor.Dialect()
	document, ok := documents[dialect.Name()]
	require.Truef(t, ok, "no fixture schema for dialect %s", dialect.Name())

	f := fixture{Harness: h, prefix: nextPrefix()}

	t.Cleanup(func() {
		// Not t.Context(): it is already cancelled by the time cleanup runs.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := sqlkit.DropTables(ctx, execer, dialect, f.tables()); err != nil {
			t.Errorf("drop the fixture schema: %s", err)
		}
	})

	require.NoError(t, sqlkit.ApplySchema(t.Context(), execer, dialect, sqlkit.RenderSchema(document, f.prefix)),
		"apply the fixture schema")

	return f
}

// tables returns the fixture's tables, prefixed, in creation order.
func (f fixture) tables() []string {
	out := make([]string, 0, len(FixtureTables))
	for _, table := range FixtureTables {
		out = append(out, f.prefix+table)
	}

	return out
}

// table returns a fixture table's quoted, prefixed name.
func (f fixture) table(name string) string {
	return f.Executor.Dialect().Quote(f.prefix + name)
}

// column returns a quoted column name.
func (f fixture) column(name string) string { return f.Executor.Dialect().Quote(name) }

// insertWidget writes one widget row inside whatever transaction ctx carries.
func (f fixture) insertWidget(ctx context.Context, t *testing.T, id, owner string) {
	t.Helper()

	w := sqlkit.NewWriter(f.Executor.Dialect())
	w.Write("INSERT INTO ", f.table("widgets"), " (", f.column("id"), ", ", f.column("owner"), ") ")
	w.Write("VALUES (", w.BindAll(id, owner), ")")

	matched, err := f.Executor.Exec(ctx, w.Done())
	require.NoErrorf(t, err, "insert widget %s", id)
	assert.Equal(t, int64(1), matched)
}

// countWidgets counts the widgets with the given ids visible on ctx.
func (f fixture) countWidgets(ctx context.Context, t *testing.T, ids ...string) int64 {
	t.Helper()

	w := sqlkit.NewWriter(f.Executor.Dialect())
	w.Write("SELECT COUNT(*) FROM ", f.table("widgets"), " WHERE ", f.column("id"), " IN (")
	w.Write(w.BindAll(stringsToAny(ids)...), ")")

	var count int64

	err := f.Executor.Query(ctx, w.Done(), func(rows sqlkit.Rows) error {
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return err
			}

			decoded, err := sqlkit.DecodeInt(raw)
			if err != nil {
				return err
			}

			count = decoded
		}

		return nil
	})
	require.NoError(t, err, "count widgets")

	return count
}

// queryStrings runs a one-column query and reads every row as text.
func (f fixture) queryStrings(t *testing.T, statement sqlkit.Statement) []string {
	t.Helper()

	var values []string

	require.NoError(t, f.Executor.Query(t.Context(), statement, func(rows sqlkit.Rows) error {
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return err
			}

			value, err := sqlkit.DecodeString(raw)
			if err != nil {
				return err
			}

			values = append(values, value)
		}

		return nil
	}))

	return values
}

// stringsToAny turns strings into bind values.
func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}

	return out
}

// runStatementCases covers how statements run and how rows are released.
func runStatementCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a write returns the rows it matched, not the rows it changed", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		f.insertWidget(t.Context(), t, "w-1", "alice")
		f.insertWidget(t.Context(), t, "w-2", "alice")
		f.insertWidget(t.Context(), t, "w-3", "bob")

		w := sqlkit.NewWriter(f.Executor.Dialect())
		w.Write("UPDATE ", f.table("widgets"), " SET ", f.column("score"), " = ", f.column("score"))
		w.Write(" WHERE ", f.column("owner"), " = ", w.Bind("alice"))

		matched, err := f.Executor.Exec(t.Context(), w.Done())
		require.NoError(t, err)
		assert.Equal(t, int64(2), matched,
			"a conditional update that wrote the same values must still report its match, "+
				"or it reads as a conflict")
	})

	t.Run("the zero statement is a no-op", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		matched, err := f.Executor.Exec(t.Context(), sqlkit.Statement{})
		require.NoError(t, err)
		assert.Zero(t, matched)

		called := false

		require.NoError(t, f.Executor.Query(t.Context(), sqlkit.Statement{}, func(sqlkit.Rows) error {
			called = true

			return nil
		}))
		assert.False(t, called, "scan must not run for a statement that was never run")
	})

	t.Run("a query hands every row to scan, in order", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		for _, id := range []string{"w-3", "w-1", "w-2"} {
			f.insertWidget(t.Context(), t, id, "alice")
		}

		w := sqlkit.NewWriter(f.Executor.Dialect())
		w.Write("SELECT ", f.column("id"), " FROM ", f.table("widgets"), " ORDER BY ", f.column("id"))

		ids := f.queryStrings(t, w.Done())

		assert.Equal(t, []string{"w-1", "w-2", "w-3"}, ids)
	})

	t.Run("rows are released after a failed scan", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		f.insertWidget(t.Context(), t, "w-1", "alice")
		f.insertWidget(t.Context(), t, "w-2", "alice")

		w := sqlkit.NewWriter(f.Executor.Dialect())
		w.Write("SELECT ", f.column("id"), " FROM ", f.table("widgets"))
		statement := w.Done()

		// Far more failed scans than any harness's pool holds connections. An
		// executor that leaked the rows of a failed scan would exhaust the pool
		// and the query after the loop would never get a connection.
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()

		for range 64 {
			err := f.Executor.Query(ctx, statement, func(rows sqlkit.Rows) error {
				rows.Next()

				return errInjected
			})
			require.ErrorIs(t, err, errInjected, "the scan's own error is returned")
		}

		assert.Equal(t, int64(2), f.countWidgets(ctx, t, "w-1", "w-2"),
			"a connection must still be available after many failed scans")
	})

	t.Run("an execution error names the statement", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		missing := f.Executor.Dialect().Quote(f.prefix + "no_such_table")

		_, err := f.Executor.Exec(t.Context(), sqlkit.Statement{SQL: "DELETE FROM " + missing})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DELETE FROM "+missing)

		err = f.Executor.Query(t.Context(), sqlkit.Statement{SQL: "SELECT 1 FROM " + missing},
			func(sqlkit.Rows) error { return nil })
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SELECT 1 FROM "+missing)
	})

	t.Run("statements built with the executor's dialect bind correctly", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		f.insertWidget(t.Context(), t, "w-1", "alice")
		f.insertWidget(t.Context(), t, "w-2", "bob")

		assert.Equal(t, int64(2), f.countWidgets(t.Context(), t, "w-1", "w-2", "w-9"),
			"three bind markers in one IN list must reach the database as three values")
	})
}

// runTransactionCases covers who begins, who commits, and what nesting means.
func runTransactionCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("an executor-led scope commits before returning", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		assert.False(t, f.Executor.InTransaction(t.Context()), "a bare context carries no transaction")

		require.NoError(t, f.Executor.Do(t.Context(), func(ctx context.Context) error {
			assert.True(t, f.Executor.InTransaction(ctx), "the scope's context carries its transaction")
			f.insertWidget(ctx, t, "w-1", "alice")

			return nil
		}))

		assert.Equal(t, int64(1), f.countWidgets(t.Context(), t, "w-1"),
			"a scope the executor began must be durable when Do returns")
	})

	t.Run("a caller-led scope is joined and not committed by the executor", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		scoped, done := f.CallerTx(t.Context())

		require.NoError(t, f.Executor.Do(scoped, func(ctx context.Context) error {
			assert.True(t, f.Executor.InTransaction(ctx), "the executor must see that it joined")
			f.insertWidget(ctx, t, "w-1", "alice")

			return nil
		}))

		assert.Equal(t, int64(0), f.countWidgets(t.Context(), t, "w-1"),
			"the executor must not commit a transaction it did not begin")

		require.NoError(t, done(true))
		assert.Equal(t, int64(1), f.countWidgets(t.Context(), t, "w-1"))
	})

	t.Run("a caller-led scope the caller discards leaves nothing", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		scoped, done := f.CallerTx(t.Context())

		require.NoError(t, f.Executor.Do(scoped, func(ctx context.Context) error {
			f.insertWidget(ctx, t, "w-1", "alice")

			return nil
		}))

		require.NoError(t, done(false))
		assert.Equal(t, int64(0), f.countWidgets(t.Context(), t, "w-1"))
	})

	t.Run("a nested scope joins rather than nests", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		require.NoError(t, f.Executor.Do(t.Context(), func(ctx context.Context) error {
			f.insertWidget(ctx, t, "w-outer", "alice")

			return f.Executor.Do(ctx, func(ctx context.Context) error {
				// The inner scope can only see the outer scope's uncommitted
				// write if there is one transaction and not two.
				assert.Equal(t, int64(1), f.countWidgets(ctx, t, "w-outer"))
				f.insertWidget(ctx, t, "w-inner", "alice")

				return nil
			})
		}))

		assert.Equal(t, int64(2), f.countWidgets(t.Context(), t, "w-outer", "w-inner"),
			"both writes commit together")
	})

	t.Run("an inner failure aborts the whole scope", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		err := f.Executor.Do(t.Context(), func(ctx context.Context) error {
			f.insertWidget(ctx, t, "w-outer", "alice")

			return f.Executor.Do(ctx, func(ctx context.Context) error {
				f.insertWidget(ctx, t, "w-inner", "alice")

				return errInjected
			})
		})
		require.ErrorIs(t, err, errInjected)

		assert.Equal(t, int64(0), f.countWidgets(t.Context(), t, "w-outer", "w-inner"),
			"a savepoint would have let the outer write survive its sibling's failure")
	})

	t.Run("a nested scope takes no savepoint", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		err := f.Executor.Do(t.Context(), func(ctx context.Context) error {
			f.insertWidget(ctx, t, "w-outer", "alice")

			innerErr := f.Executor.Do(ctx, func(ctx context.Context) error {
				f.insertWidget(ctx, t, "w-inner", "alice")

				return errInjected
			})
			require.ErrorIs(t, innerErr, errInjected)

			// This is the whole difference between joining and nesting: a
			// savepoint would have discarded the inner write on the inner
			// scope's failure. Joining leaves both writes in one transaction
			// that is still entirely undecided.
			assert.Equal(t, int64(1), f.countWidgets(ctx, t, "w-inner"),
				"a failed inner scope must not have discarded its own write behind a savepoint")

			return innerErr
		})
		require.ErrorIs(t, err, errInjected)

		assert.Equal(t, int64(0), f.countWidgets(t.Context(), t, "w-outer", "w-inner"))
	})
}

// runRollbackCases covers what must not survive a scope that failed.
func runRollbackCases(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name string
		// ctx derives the context the scope runs on; cancel, when not nil, is
		// called from inside the scope after its write.
		ctx func(ctx context.Context) (context.Context, context.CancelFunc)
		// act is the scope's last step after its write.
		act    func() error
		assert func(t *testing.T, panicked any, err error)
	}

	cases := []testCase{
		{
			name: "an error rolls the scope back",
			act:  func() error { return errInjected },
			assert: func(t *testing.T, panicked any, err error) {
				require.ErrorIs(t, err, errInjected)
				assert.Nil(t, panicked)
			},
		},
		{
			name: "a panic rolls the scope back and is re-raised unchanged",
			act:  func() error { panic("sqlkittest: injected panic") },
			assert: func(t *testing.T, panicked any, _ error) {
				assert.Equal(t, "sqlkittest: injected panic", panicked,
					"a panic must reach the caller unchanged, not become an error")
			},
		},
		{
			name: "a context cancelled inside the scope rolls it back",
			ctx:  context.WithCancel,
			act:  func() error { return nil },
			assert: func(t *testing.T, panicked any, err error) {
				assert.Nil(t, panicked)
				require.ErrorIs(t, err, context.Canceled,
					"work whose caller has gone must not be committed")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, factory, fixtureSchemas)

			ctx, cancel := t.Context(), context.CancelFunc(nil)
			if tc.ctx != nil {
				ctx, cancel = tc.ctx(ctx)
				defer cancel()
			}

			var (
				panicked any
				err      error
			)

			func() {
				defer func() { panicked = recover() }()

				err = f.Executor.Do(ctx, func(scoped context.Context) error {
					f.insertWidget(scoped, t, "w-1", "alice")

					if cancel != nil {
						cancel()
					}

					return tc.act()
				})
			}()

			tc.assert(t, panicked, err)
			assert.Equal(t, int64(0), f.countWidgets(t.Context(), t, "w-1"), "no partial change may be durable")
		})
	}
}

// runValueCases covers values that must survive the round trip identically on
// every driver and dialect.
func runValueCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a payload survives byte for byte", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		payload := []byte(`{"b": 1,  "a": [1.0, 2e3], "c": "é"}`)

		w := sqlkit.NewWriter(f.Executor.Dialect())
		w.Write("INSERT INTO ", f.table("widgets"), " (", f.column("id"), ", ", f.column("owner"), ", ")
		w.Write(f.column("payload"), ") VALUES (", w.BindAll("w-1", "alice", sqlkit.EncodeRaw(payload)), ")")

		_, err := f.Executor.Exec(t.Context(), w.Done())
		require.NoError(t, err)

		read := sqlkit.NewWriter(f.Executor.Dialect())
		read.Write("SELECT ", f.column("payload"), " FROM ", f.table("widgets"), " WHERE ", f.column("id"), " = ")
		read.Write(read.Bind("w-1"))

		var got []byte

		require.NoError(t, f.Executor.Query(t.Context(), read.Done(), func(rows sqlkit.Rows) error {
			if !rows.Next() {
				return nil
			}

			var raw any
			if err := rows.Scan(&raw); err != nil {
				return err
			}

			decoded, err := sqlkit.DecodeJSON(raw)
			got = decoded

			return err
		}))

		assert.Equal(t, string(payload), string(got), "key order, whitespace and number literals must survive")
	})

	t.Run("an instant survives to the microsecond, in UTC", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		instant := time.Date(2026, 9, 14, 19, 30, 1, 123456789, time.FixedZone("WIB", 7*60*60))

		f.insertWidget(t.Context(), t, "w-1", "alice")
		f.setWidgetTime(t, "w-1", instant)

		assert.Equal(t, sqlkit.NormalizeTime(instant), f.widgetTimes(t)[0])
	})

	t.Run("timestamps order chronologically", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

		instants := map[string]time.Time{
			"w-late":    base.Add(time.Hour),
			"w-early":   base.Add(-24 * time.Hour),
			"w-precise": base.Add(time.Microsecond),
			"w-base":    base,
		}

		for id, instant := range instants {
			f.insertWidget(t.Context(), t, id, "alice")
			f.setWidgetTime(t, id, instant)
		}

		assert.Equal(t, []time.Time{
			base.Add(-24 * time.Hour), base, base.Add(time.Microsecond), base.Add(time.Hour),
		}, f.widgetTimes(t), "ordering by the column must be chronological, even stored as text")
	})

	t.Run("identifiers compare case-sensitively", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		f.insertWidget(t.Context(), t, "w-1", "alice")
		f.insertWidget(t.Context(), t, "w-2", "Alice")

		w := sqlkit.NewWriter(f.Executor.Dialect())
		w.Write("SELECT ", f.column("id"), " FROM ", f.table("widgets"), " WHERE ", f.column("owner"), " = ")
		w.Write(w.Bind("alice"))

		ids := f.queryStrings(t, w.Done())

		assert.Equal(t, []string{"w-1"}, ids, "alice must not match Alice on any dialect")
	})
}

// setWidgetTime stores an instant in a widget's timestamp column.
func (f fixture) setWidgetTime(t *testing.T, id string, instant time.Time) {
	t.Helper()

	w := sqlkit.NewWriter(f.Executor.Dialect())
	w.Write("UPDATE ", f.table("widgets"), " SET ", f.column("at"), " = ",
		w.Bind(sqlkit.EncodeTime(f.Executor.Dialect(), &instant)))
	w.Write(" WHERE ", f.column("id"), " = ", w.Bind(id))

	matched, err := f.Executor.Exec(t.Context(), w.Done())
	require.NoError(t, err)
	require.Equal(t, int64(1), matched)
}

// widgetTimes reads every widget's instant, ordered by the timestamp column.
func (f fixture) widgetTimes(t *testing.T) []time.Time {
	t.Helper()

	w := sqlkit.NewWriter(f.Executor.Dialect())
	w.Write("SELECT ", f.column("at"), " FROM ", f.table("widgets"), " ORDER BY ", f.column("at"))

	var instants []time.Time

	require.NoError(t, f.Executor.Query(t.Context(), w.Done(), func(rows sqlkit.Rows) error {
		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return err
			}

			instant, err := sqlkit.DecodeTime(raw)
			if err != nil {
				return err
			}

			instants = append(instants, instant)
		}

		return nil
	}))

	return instants
}

// runSchemaCases covers rendering, the development runner and verification on
// a live database.
func runSchemaCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("the fixture renders, applies and verifies", func(t *testing.T) {
		f := newFixture(t, factory, fixtureSchemas)

		querier, ok := f.Executor.(sqlkit.Querier)
		require.True(t, ok)

		assert.NoError(t, sqlkit.VerifySchema(t.Context(), querier, f.Executor.Dialect(), f.prefix, FixtureExpectation),
			"the schema published for a dialect must satisfy the verification run against it")
	})

	t.Run("a broken schema reports every discrepancy", func(t *testing.T) {
		f := newFixture(t, factory, brokenSchemas)

		querier, ok := f.Executor.(sqlkit.Querier)
		require.True(t, ok)

		err := sqlkit.VerifySchema(t.Context(), querier, f.Executor.Dialect(), f.prefix, FixtureExpectation)

		var schemaErr *sqlkit.SchemaError

		require.ErrorAs(t, err, &schemaErr)
		assert.ErrorIs(t, err, sqlkit.ErrConfiguration)

		issues := make([]string, 0, len(schemaErr.Issues))
		for _, issue := range schemaErr.Issues {
			issues = append(issues, issue.String())
		}

		assert.ElementsMatch(t, []string{
			f.prefix + "parts: table is missing",
			f.prefix + "widgets.score: column is missing",
			f.prefix + "widgets: index " + f.prefix + "widgets_owner_idx is missing",
		}, issues, "every discrepancy, in one error: %s", strings.Join(issues, "; "))
	})
}
