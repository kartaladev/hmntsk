// Package websocket serves a recipient's notification change signals over a
// WebSocket connection, as an alternative to the server-sent event stream of
// [github.com/kartaladev/hmntsk/notify.Handler].
//
// It applies the stream's rules: the subscription is authorized by the same
// replaceable policy, a signal carries only the kind of change and when it
// happened, and every connection subscribes through the same
// [github.com/kartaladev/hmntsk/notify.Hub], so one per-recipient cap counts
// streams and WebSocket connections together. Beyond the stream, a client can
// mark its notifications read over the connection.
//
// Every refusal is answered as an HTTP status before the connection is
// upgraded. Browser origins other than the request's own host are refused by
// default.
//
// The handler is a standard library [net/http.Handler]. Fiber hosts cannot use
// it: Fiber's adaptor cannot hand over the hijacked connection a WebSocket needs,
// so they use the server-sent event stream instead.
package websocket
