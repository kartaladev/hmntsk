## Why

The transport audit (`VERIFIED-FINDINGS.md`, section I) confirmed, each with a failing test, that the three bindings do not serve one contract. The same request gets a different answer on net/http, gin and Fiber:
- a body over the limit is truncated on gin and answered in plain text on Fiber;
- a gzip body is decoded on Fiber only;
- a path that differs by case, a trailing slash, a double slash or `HEAD` is served, redirected or refused depending on the binding;
- some client-supplied task ids can be created but never read.

The audit also found two leaks and two wiring traps:
- a directory failure's cause, with its host and bind DN, is sent to the client in the `500` body;
- a case-folded duplicate key (`"Type"`) silently overrides `"type"`;
- net/http's `Mount` claims the host's `/`, so the host panics;
- `WithBasePath("v2")` is accepted, and then no route answers.

The contract's first requirement is that a client cannot tell the bindings apart. Today that isn't true.

## What Changes

- **Server errors reveal nothing (T7).** A `500` body carries a fixed message, including for a group-resolution failure. The cause goes to a new server-side hook, `transportcore.WithInternalErrorHandler`. Its default logs through `slog.Default()`.
- **One body limit, one `413` (T8).**
  - Every binding reads at most `transportcore.DefaultMaxBodyBytes` (8 MiB) by default, and answers a larger body with the contract's JSON `413`. It never truncates the body.
  - gin uses `http.MaxBytesReader`.
  - Fiber gains `WithMaxBodyBytes`. `App()` sizes Fiber's `BodyLimit` to match, and a new `fibertransport.ErrorHandler` answers Fiber's own `413` in the contract shape.
  - A non-positive limit becomes a configuration error on every binding. **BREAKING** (pre-tag): it used to be ignored silently.
- **Content codings are not decoded (T9).** A body sent with a `Content-Encoding` other than `identity` is refused with `400` on every binding. The Fiber binder reads the raw body. Decompression is the host's job, in its own middleware.
- **Client-supplied ids are addressable (T10).**
  - By default, the contract refuses a create whose `id` is not 1–128 URL-unreserved characters, is `.` or `..`, or is a reserved route segment (`count`).
  - A host replaces the charset rule with `transportcore.WithTaskIDPolicy`. The reserved segments and `/` stay refused, because they are never addressable.
  - The Fiber binder percent-decodes path parameters like the other two bindings.
- **Path matching is exact and identical (T11).**
  - Paths are case-sensitive.
  - A trailing slash or an empty segment is a different path.
  - `HEAD` is not served.
  - Each of these answers the contract's JSON `404` on every convenience constructor. Where a host owns the router, the stated limits in design.md apply.
- **net/http `Mount` leaves the host's `/` alone (T12).**
  - It registers its not-found handler only under the base path.
  - A pattern that conflicts with the host's is returned as a configuration error, not a panic.
  - A new `httptransport.NotFoundHandler()` is available to hosts.
- **`WithBasePath` is validated (T13).** A base path that is empty, has no leading `/`, has empty, `.` or `..` segments, or has characters outside the unreserved set is a `ConfigurationError` from `transportcore.New`. **BREAKING** (pre-tag): `""` and `"/"` were silently ignored.
- **Request bodies decode strictly (T14a, T14b).**
  - A key that matches a contract field only case-insensitively, and a duplicated key, are refused with `400`.
  - Unknown fields are refused with `400` by default. **BREAKING** (pre-tag).
  - `transportcore.WithUnknownFields(transportcore.UnknownFieldsIgnore)` restores the lenient behaviour.
  - Consumer-owned payloads (`input`, `output`, `patch`, correlation `extra`, callback `referenceParameters`) are never inspected.

## Non-goals

- Authorization of create and lifecycle operations (change `lifecycle-authorization`).
- Validation of `deadlineSeconds` and `priority` ranges (T15, change `engine-input-validation`).
- Post-commit dispatch (T6, change `post-commit-dispatch`).

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-http-api`: server errors are masked, and request bodies are bounded, decoded strictly and never content-decoded. Client-supplied ids are validated for addressability, path matching is exact on every binding, the base path is validated at construction, and net/http's `Mount` coexists with a host `/` route.

## Impact

- **Code:**
  - `transport/core`: `api.go`, `errors.go`, `seam.go` and a new request-decoding file.
  - `transport/http`: `handler.go`.
  - `transport/gin`: `router.go`.
  - `transport/fiber`: `app.go`.
  - `transporttest`: new shared cases, and each binding's harness.
- **API (additive):**
  - `transportcore`: `WithInternalErrorHandler`, `WithTaskIDPolicy`, `DefaultTaskIDPolicy`, `WithUnknownFields`, `UnknownFieldsReject`/`UnknownFieldsIgnore`, `DefaultMaxBodyBytes`, `StatusPayloadTooLarge`, `PayloadTooLargeResponse`, and `Request.ContentEncoding`.
  - `fibertransport`: `WithMaxBodyBytes`, `MaxBodyBytes` and `ErrorHandler`.
  - `httptransport`: `NotFoundHandler` and `CanonicalPaths`.
- **Behaviour (pre-tag, recorded per `library-design.md` §7):**
  - unknown request fields are refused;
  - a non-positive body limit and an invalid base path fail construction;
  - a group-resolution `500` no longer carries its cause;
  - `HEAD` on a contract route is a `404` on net/http and Fiber.
- **Docs:** binding godoc (router settings a host keeps for parity) and the OpenAPI document (`413`, id pattern).
