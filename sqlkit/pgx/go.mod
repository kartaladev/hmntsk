module github.com/kartaladev/hmntsk/sqlkit/pgx

go 1.26.0

// The dependencies on github.com/kartaladev/hmntsk/sqlkit and
// github.com/kartaladev/hmntsk/sqlkit/sqlkittest are supplied by the
// repository's go.work during development and are written in here, with real
// versions, when sqlkit is tagged in its own repository.

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
