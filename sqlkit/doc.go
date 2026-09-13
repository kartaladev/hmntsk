// Package sqlkit is dialect-portable SQL support that knows nothing about any
// domain.
//
// It owns what varies by database and nothing that varies by application: the
// three dialects, statements and their bind markers, the text and time codecs
// that make PostgreSQL, MySQL and SQLite read back the same values, schema
// rendering, a development migration runner, schema verification against an
// expectation the caller supplies, and the [Executor] contract that the driver
// modules implement for database/sql, pgx and GORM.
//
// A store written once against [Executor] runs natively on all three drivers.
// The stores built on sqlkit decide their own tables and statements; sqlkit
// never names one.
//
// sqlkit imports nothing from any other module of the repository it was
// extracted from, and never will: it is built to move to its own repository.
package sqlkit
