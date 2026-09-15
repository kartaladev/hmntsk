Most of this change was built test-first before the proposal was written, and
is uncommitted in the working tree. Ticked tasks were verified in that session;
the apply phase re-runs their checks before ticking the remaining ones.

## 1. Purchasing model and records

- [x] 1.1 Declare the demo's own task types, groups, people, route, form keys and supplier registry in `purchasing.go`, dropping the `examples/internal/invoicing` import from `contextual-ui`; verify `go vet ./contextual-ui` passes and no file imports `internal/invoicing`
- [x] 1.2 Add `store.go` with orders (status, registration, invoice ID, expected time), invoices and documents that join the host transaction; verify through the record and workflow tests in 2.x and 3.x

## 2. Workflow

- [x] 2.1 Open a placed order with its approval task in one transaction, and seed eight orders across every stage; verify `TestServerRoutes` order-listing and record cases pass
- [x] 2.2 Rewrite the `order-workflow` sink for approve-order, send/upload, review and approve-invoice completions with conditional moves and derived task IDs; verify every `TestWorkflowSinkDelivery` case passes, including repeated and failed-update deliveries
- [x] 2.3 Receive due invoices in one transaction with their review, run every second beside the relay, with `config.InvoiceDelay` defaulting to a random 5 to 20 seconds; verify `TestReceiveDueInvoices` passes

## 3. Demo HTTP API

- [x] 3.1 Add `GET /demo/suppliers`, `GET /demo/orders/{id}` (sorted by creation time) and remove `GET /demo/invoices/{id}`; verify the corresponding `TestServerRoutes` cases pass
- [x] 3.2 Add the send and upload endpoints that store the document and complete the task in one transaction, with content-sniffed upload types and the size limit, and the engine's errors mapped to 404, 403, 409 and 400; verify the refusal cases in `TestServerRoutes` pass
- [x] 3.3 Add `GET /demo/documents/{id}` served as an attachment with `nosniff` and a sandboxing CSP; verify the download cases in `TestServerRoutes` and `TestOrderWorkflow` pass
- [x] 3.4 Cover the whole flow end to end for a sent, an uploaded, a declined and a disputed order; verify `go test -race -count=1 ./contextual-ui` passes from `examples/`

## 4. Page

- [x] 4.1 Add `@mui/x-data-grid` pinned to 9.13.0 with theme defaults; verify `npm ls @mui/x-data-grid` in `web/` and `make ui-audit` report no high vulnerability
- [x] 4.2 Replace the invoice route with `/orders/{id}/{activity}?task=`, and derive the seven-step workflow from the order record; verify `pages.test.ts` and `steps.test.ts` pass
- [x] 4.3 Add cursor paging and upload checks as pure functions; verify `paging.test.ts` and `purchaseOrder.test.ts` pass
- [x] 4.4 Rebuild the orders page, inbox and new order page on data grids, with the inbox paging on the server and no sorting or filtering; verify `make ui-build` typechecks and in a browser the inbox shows the bucket count as its row count
- [x] 4.5 Add the purchase order form chosen by `hmntsk.formKey` in the task panel; verify in a browser that uploading a PDF completes the task, lists the document, and the invoice arrives on the open page without a reload
- [x] 4.6 Commit the rebuilt `dist/`; verify `make ui-build && git diff --exit-code examples/contextual-ui/dist` shows no change

## 5. Docs and checks

- [x] 5.1 Update `examples/contextual-ui/README.md` and the `contextual-ui` row and test note in `examples/README.md`; verify both describe the approval, purchase order and invoice flow and the new API
- [x] 5.2 Run `make lint` and `make ui-test` from the repository root; verify both pass with the pinned toolchain
- [x] 5.3 Run `openspec validate contextual-ui-purchase-order-flow --strict`; verify it reports the change valid
- [ ] 5.4 Commit the change on a feature branch and open a pull request; verify the `ci` workflow, including the generated-artefact diff and `examples ui` jobs, passes
