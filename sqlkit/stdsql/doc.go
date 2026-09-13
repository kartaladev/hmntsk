// Package stdsqlexec is the [sqlkit.Executor] over database/sql.
//
// It runs statements through a *sql.DB, or through the *sql.Tx a context
// carries, and gives transactions the contract every executor shares: whoever
// begins commits, a nested scope joins and flattens rather than taking a
// savepoint, and an error, a panic or a cancelled context rolls back.
package stdsqlexec
