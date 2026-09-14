package demo

import (
	"fmt"
	"strings"
)

// RequireEnv reads the address of a service a service-backed scenario needs,
// such as a database DSN or a broker URL, from the environment through getenv
// (os.Getenv in a program).
//
// A scenario's test provisions the service in a container and passes the
// address in directly; a reader running the program starts the service
// themselves. When the setting is missing or blank, the error names it and
// says how to start the service, so the reader knows what to do next.
func RequireEnv(getenv func(string) string, name, start string) (string, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is not set; start the service with:\n\n  %s\n\nthen set %s and run again", name, start, name)
	}

	return value, nil
}
