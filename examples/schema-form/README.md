# schema-form

Doing an invoice approval the way a generic form does: from the schemas the
task type serves over HTTP, without knowing anything about invoices.

## What it shows

- **Reading the schema:** `GET /v1/task-types/{name}` serves the type's title,
  input and output schemas, and metadata.
- **Rendering fields:** a minimal renderer turns the output schema's top-level
  properties into form fields, with their types, enums and required marks.
- **Saving progress:** an RFC 6902 JSON Patch is merged into saved progress. It
  is never checked for completeness, so a half-filled form survives a reload.
- **Completing:** the output is checked in full against the output schema.
  - A missing required field is refused with 400 `validation_failed`.
  - The complete output is accepted.
- **Override:** the typed facade `hmntsk.Define[In, Out]`.
  - Schemas are derived from the host's own Go types.
  - Every field that is neither a pointer nor `omitempty` is required.
  - Input and output are read back as typed values.
  - A typed completion handler, `Kind.OnCompleted`, receives the decoded
    output. It is an ordinary event handler registered with
    `hmntsk.WithEventHandlers`.

## Context

The shared invoice-approval domain.

- **Tasks:** an `invoice.approve` output needs `approved` and a `reason`, one of
  within-budget, over-budget, duplicate or other.
- **People:** alice works the generic form; bob completes the typed task.

## What it leaves out

- **A browser form:** the rendered page is in `inbox-ui`.
- **Other lifecycle operations:** release, delegate and fail are in
  `lifecycle-operations`.

## Run it

From the `examples/` directory:

```sh
go run ./schema-form
go test ./schema-form
```

No Docker is needed.

## Read next

- [Task types](../../README.md) in the root README
- The `Example_typedFacade` example in
  [`example_test.go`](../../example_test.go)
