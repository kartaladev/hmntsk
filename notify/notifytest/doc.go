// Package notifytest is the conformance suite every [notify.Store] must pass.
//
// One suite, run against the in-memory store and against notify/sqlstore on
// every supported driver and dialect, is what makes "identical on every store"
// a fact rather than an intention. A host that writes its own store runs the
// same suite against it.
package notifytest
