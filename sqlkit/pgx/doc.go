// Package pgxexec is the [sqlkit.Executor] over jackc/pgx.
//
// It runs statements through a *pgxpool.Pool, or through the pgx.Tx a context
// carries, with the transaction contract every executor shares. pgx speaks only
// PostgreSQL, so the executor's dialect is always [sqlkit.PostgreSQL].
package pgxexec
