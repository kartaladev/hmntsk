// Package gormexec is the [sqlkit.Executor] over GORM.
//
// Its job is transaction participation, not object mapping: it runs raw
// statements through the host's *gorm.DB, so that writes made through sqlkit
// and writes made through GORM can commit together. It declares no models and
// never calls AutoMigrate.
//
// Two decisions differ from GORM's defaults. Statements carry GORM's own "?"
// bind marker on every database, because GORM rewrites it for the dialector;
// [Executor.Dialect] reports that, so a store cannot build a statement with the
// wrong marker. And nested scopes flatten into one transaction rather than
// taking the SAVEPOINT db.Transaction would, so an inner failure aborts the
// whole scope exactly as it does on every other executor.
package gormexec
