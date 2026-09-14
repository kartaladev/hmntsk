// Package tasknotify turns hmntsk task events into notify notifications.
//
// It is the one module that knows both worlds. The engine notifies nobody, and
// notify knows nothing about tasks; a [Projector] sits between them as a relay
// sink, so that every notification an event should produce survives a crash, a
// redeploy and a retried delivery.
//
// By default a pooled task is offered to its candidates, the other candidates
// are told when someone takes it, a reserved or delegated task is assigned to
// its holder, and every notification closes when the task reaches a closing
// status. Every one of those rules is replaceable through [WithRules].
package tasknotify
