// Package redis carries notification change signals between application
// instances over Redis publish/subscribe, as a
// [github.com/kartaladev/hmntsk/notify.Broadcaster].
//
// Every instance that shares a channel receives every signal broadcast on it, so
// a notification stored through one instance reaches a client connected to
// another. A message carries only recipients, changes and their times, in the
// notify signal format; never a notification's title, links, data, kind or
// subject.
//
// Listening reports ready only once the broker has confirmed the subscription,
// so a hub running over this broadcaster accepts streams only when a signal
// broadcast from then on will reach them.
//
// Broadcasting is best effort. An unreachable broker never fails the
// notification write that produced a signal, signals broadcast while an
// instance is disconnected are not replayed to it, and the notification store
// remains the source of truth.
//
// This is not the delivery/redis sink of the task engine. That sink appends
// durable events to a stream; this broadcaster publishes ephemeral signals on a
// pub/sub channel, and the two default names do not overlap.
package redis
