// Command hmntsk-schema prints the DDL the hmntsk engine needs, for one
// dialect, so that it can be piped into a migration file.
//
// The engine never applies this itself in normal operation; your migration tool
// does.
//
//	go run github.com/kartaladev/hmntsk/store/sqlcore/cmd/hmntsk-schema \
//	    -dialect postgres -prefix hmntsk_ > migrations/0001_hmntsk.up.sql
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kartaladev/hmntsk/store/sqlcore"
)

func main() {
	dialectName := flag.String("dialect", "", "postgres, mysql or sqlite")
	prefix := flag.String("prefix", "", "prefix for every table the engine owns")
	flag.Parse()

	if err := run(*dialectName, *prefix); err != nil {
		fmt.Fprintln(os.Stderr, "hmntsk-schema:", err)
		os.Exit(1)
	}
}

// run writes one dialect's schema to standard output.
func run(dialectName, prefix string) error {
	if dialectName == "" {
		return fmt.Errorf("a -dialect is required: %s", strings.Join(dialectNames(), ", "))
	}

	dialect, ok := sqlcore.DialectByName(dialectName)
	if !ok {
		return fmt.Errorf("unsupported dialect %q; supported: %s",
			dialectName, strings.Join(dialectNames(), ", "))
	}

	source, err := sqlcore.New(dialect, sqlcore.WithTablePrefix(prefix)).MigrationsSource()
	if err != nil {
		return err
	}

	_, err = os.Stdout.WriteString(source)

	return err
}

// dialectNames lists the supported dialects for error messages.
func dialectNames() []string {
	names := make([]string, 0, len(sqlcore.Dialects()))
	for _, dialect := range sqlcore.Dialects() {
		names = append(names, dialect.Name())
	}

	return names
}
