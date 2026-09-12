module github.com/kartaladev/hmntsk/store/pgx

go 1.26

// The dependencies on github.com/kartaladev/hmntsk,
// github.com/kartaladev/hmntsk/store/sqlcore and
// github.com/kartaladev/hmntsk/storetest are supplied by the repository's
// go.work during development and are written in here, with real versions, when
// the modules are tagged.

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.11.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	golang.org/x/text v0.29.0 // indirect
)
