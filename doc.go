// Package hmntsk implements an embeddable human task engine.
//
// The engine owns the lifecycle of a human task — its states, the operations
// that move between them, the type registry that describes each kind of work,
// and the events it publishes. It owns nothing above that: the host
// application supplies the database handle, the transaction boundary, the
// identity system and the HTTP transport through the ports declared here.
//
// Nothing in this package imports a database driver or a web framework; see
// the store/* and transport/* modules for those bindings.
package hmntsk
