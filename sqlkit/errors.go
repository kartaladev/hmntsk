package sqlkit

import (
	"errors"
	"fmt"
)

// ErrConfiguration matches every wiring mistake sqlkit reports: a nil handle or
// dialect given to an executor, and a live schema a store cannot run against.
// Both are found before traffic, at construction or at startup.
var ErrConfiguration = errors.New("sqlkit: invalid configuration")

// ErrSchemaMismatch matches a [*SchemaError]. It also matches
// [ErrConfiguration], because a schema that does not meet its expectation is a
// wiring problem, discovered at startup.
var ErrSchemaMismatch = fmt.Errorf("%w: the live schema does not match what is expected", ErrConfiguration)

// ConfigurationError reports a contradictory or meaningless configuration,
// such as a nil database handle. It matches [ErrConfiguration].
type ConfigurationError struct {
	// Detail says what is wrong and, where there is one, what to pass instead.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string { return "sqlkit: " + e.Detail }

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }
