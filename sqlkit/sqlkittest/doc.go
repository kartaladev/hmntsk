// Package sqlkittest proves that an [sqlkit.Executor] behaves like every other.
//
// It holds the executor conformance suite, a tiny fixture schema per dialect
// that exercises rendering, the development runner and verification without
// any domain, and the container helpers that provision the databases the suite
// runs against.
//
// It is its own module so that sqlkit itself never depends on testcontainers.
package sqlkittest
