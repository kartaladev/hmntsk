// Package nats publishes hmntsk events to NATS, on plain subjects or to
// JetStream.
//
// It offers two [github.com/kartaladev/hmntsk/relay.Sink]s, because hosts use
// NATS in two ways and the two cannot promise the same thing:
//
//   - [NewSink] publishes to plain subjects. Delivered means the server received
//     the message, and nothing more: an event published while no subscriber
//     matches its subject is delivered and lost.
//   - [NewJetStreamSink] publishes to JetStream. Delivered means a stream stored
//     the message, and the stream discards a redelivery inside its duplicate
//     window. The sink never creates a stream: with none capturing the subject,
//     every attempt is retryable until the relay dead-letters the event.
//
// Both are producers and nothing else. They create no subscription, consumer or
// stream, and neither dials nor closes the host's connection.
//
// Every relayed event is published, whether or not the task carries a callback
// address. The bus serves internal consumers and is independent of the per-task
// callback mechanism.
//
// # The message contract
//
// Both sinks publish the same message. The subject is the prefix,
// [DefaultSubjectPrefix] unless [WithSubjectPrefix] says otherwise, a dot, and
// the event type:
//
//	hmntsk.events.task.completed
//
// so a consumer selects event types with subject wildcards, filtered by the
// server. The task type is not in the subject, because a registered type name
// may contain characters a subject cannot; filter on [HeaderTaskType].
//
// The headers carry the event identifier, the attempt's delivery identifier and
// number, the event type, the task identifier and type, the contract version
// [Schema], and each correlation value the task carries. They are routing
// hints. The body is the whole [github.com/kartaladev/hmntsk.Event] as JSON and
// is authoritative: the client replaces a line break in a header value with a
// space, and the body keeps the original.
//
// Redelivery carries the same event identifier, so a consumer de-duplicates on
// [HeaderEventID]. The relay delivers at least once.
//
// Read docs/delivery.md, under "Publishing to NATS", before choosing a mode.
package nats
