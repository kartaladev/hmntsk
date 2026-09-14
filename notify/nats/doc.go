// Package nats carries notification change signals between application
// instances over NATS core subjects, as a
// [github.com/kartaladev/hmntsk/notify.Broadcaster].
//
// Every instance subscribed to the subject receives every signal published on
// it: the subscription deliberately uses no queue group, which would hand each
// signal to only one instance. A message carries only recipients, changes and
// their times, in the notify signal format; never a notification's title,
// links, data, kind or subject.
//
// Broadcasting is best effort. An unreachable server never fails the
// notification write that produced a signal, signals published while an
// instance is disconnected are not replayed to it, and the notification store
// remains the source of truth.
//
// This is not the delivery/nats sink of the task engine. That sink publishes
// durable events under hmntsk.events; this broadcaster publishes ephemeral
// signals on a subject of its own.
package nats
