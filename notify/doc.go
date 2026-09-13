// Package notify is a durable, per-recipient notification inbox that knows
// nothing about the domain publishing into it.
//
// A publisher addresses each [Notification] to exactly one recipient, about a
// subject at a version. A notification is ACTIVE until its recipient reads it or
// its subject moves on and a publisher closes it. Publishing is idempotent per
// source and recipient, and a close remembers the version it closed at, so a
// source retried after a newer close can never reopen the subject.
//
// Everything a host touches goes through a [Service]. It stores through a
// [Store] (in memory by default; notify/sqlstore for PostgreSQL, MySQL and
// SQLite), signals changes through a [Broadcaster] (in-process by default),
// bounds storage with a [Pruner] the host drives, and serves the HTTP contract
// and its server-sent event stream through a [Handler] the host mounts.
//
// Kinds, titles, links and data belong to the publisher: the library stores and
// returns them unchanged and interprets none of them.
//
// notify imports nothing from the task engine it was built next to, and never
// will: it is built to move to its own repository.
package notify
