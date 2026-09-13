// Package sqlstore is a [notify.Store] on PostgreSQL, MySQL and SQLite.
//
// It is written once, against [sqlkit.Executor], and runs natively on each
// executor module: database/sql, pgx and GORM. The schema is published per
// dialect for the host to apply through its own migration pipeline, and
// [Store.VerifySchema] reports every discrepancy at startup.
//
// Every method runs in a transaction of its own and never joins a transaction
// the caller holds for other data.
package sqlstore
