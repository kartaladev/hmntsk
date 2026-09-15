import { describe, expect, it } from "vitest";

import type { OrderDocument, OrderRecord, OrderStatus, RecordTask } from "./api";
import { taskToWork, workflowSteps, type Step } from "./steps";

// The server says whether a task is terminal; these are the statuses hmntsk
// calls terminal, for building fixtures.
const terminal = ["COMPLETED", "FAILED", "ERROR", "EXITED", "OBSOLETE"];

const typeOf: Record<string, string> = {
  "approve-order": "purchase.approve-order",
  "purchase-order": "purchase.send-order",
  "review-invoice": "purchase.review-invoice",
  "approve-invoice": "purchase.approve-invoice",
};

const task = (id: string, activityKey: string, status: string, assignee?: string): RecordTask => ({
  id,
  type: typeOf[activityKey] ?? activityKey,
  status,
  terminal: terminal.includes(status),
  assignee,
  activityKey,
  priority: 2,
  createdAt: "2026-09-14T09:00:00Z",
});

const sent: OrderDocument = {
  id: "PO-201",
  orderId: "ORD-201",
  fileName: "PO-201.txt",
  contentType: "text/plain; charset=utf-8",
  size: 200,
  method: "sent",
  sentTo: "procurement@globex.example",
  createdBy: "erin",
  createdAt: "2026-09-14T09:05:00Z",
};

const uploaded: OrderDocument = { ...sent, fileName: "signed.pdf", contentType: "application/pdf", method: "uploaded", sentTo: undefined };

type Extras = { documents?: OrderDocument[]; invoice?: boolean; expected?: string };

const record = (status: OrderStatus, tasks: RecordTask[], extras: Extras = {}): OrderRecord => ({
  order: {
    id: "ORD-201",
    supplier: "Globex Cloud",
    supplierRegistered: true,
    description: "Laptops",
    amount: 4200,
    requestedBy: "erin",
    status,
    ...(extras.invoice ? { invoiceId: "INV-201" } : {}),
    ...(extras.expected ? { invoiceExpectedAt: extras.expected } : {}),
    createdAt: "2026-09-14T09:00:00Z",
  },
  invoice: extras.invoice
    ? { id: "INV-201", orderId: "ORD-201", supplier: "Globex Cloud", amount: 4200, receivedAt: "2026-09-14T09:10:00Z" }
    : null,
  documents: extras.documents ?? [],
  tasks,
});

// Only the states, which is what a reader of the stepper sees first.
const states = (steps: Step[]) => steps.map((s) => [s.key, s.state]);

const approvedOrder = task("ao", "approve-order", "COMPLETED", "carol");
const sentOrder = task("po", "purchase-order", "COMPLETED", "erin");
const reviewed = task("ri", "review-invoice", "COMPLETED", "alice");

describe("workflowSteps", () => {
  const cases: { name: string; record: OrderRecord; assert: (steps: Step[]) => void }[] = [
    {
      name: "a placed order waits for its budget holder",
      record: record("pending-approval", [task("ao", "approve-order", "RESERVED", "carol")]),
      assert: (steps) => {
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["approve-order", "active"],
          ["purchase-order", "waiting"],
          ["invoice", "waiting"],
          ["review-invoice", "waiting"],
          ["approve-invoice", "waiting"],
          ["outcome", "waiting"],
        ]);
        expect(steps[0]!.detail).toBe("ORD-201 by erin");
        expect(steps[1]!.detail).toBe("carol has claimed it");
      },
    },
    {
      name: "a declined order skips everything after its approval",
      record: record("declined", [approvedOrder]),
      assert: (steps) => {
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["approve-order", "done"],
          ["purchase-order", "skipped"],
          ["invoice", "skipped"],
          ["review-invoice", "skipped"],
          ["approve-invoice", "skipped"],
          ["outcome", "failed"],
        ]);
        expect(steps[6]!.label).toBe("Declined");
      },
    },
    {
      name: "an approved order waits for purchasing to issue its purchase order",
      record: record("awaiting-purchase-order", [approvedOrder, task("po", "purchase-order", "READY")]),
      assert: (steps) => {
        expect(steps[2]!.state).toBe("active");
        expect(steps[2]!.detail).toBe("waiting to be claimed");
      },
    },
    {
      name: "a sent purchase order names where it went, and the invoice is expected",
      record: record("awaiting-invoice", [approvedOrder, sentOrder], {
        documents: [sent],
        expected: "2026-09-14T09:05:20Z",
      }),
      assert: (steps) => {
        expect(steps[2]).toMatchObject({ state: "done", detail: "sent to procurement@globex.example" });
        expect(steps[3]).toMatchObject({ key: "invoice", state: "active", detail: "the supplier has the purchase order" });
      },
    },
    {
      name: "an uploaded purchase order names who uploaded it",
      record: record("awaiting-invoice", [approvedOrder, sentOrder], { documents: [uploaded] }),
      assert: (steps) => expect(steps[2]).toMatchObject({ state: "done", detail: "uploaded by erin" }),
    },
    {
      name: "an arrived invoice waits for its review",
      record: record("invoice-review", [approvedOrder, sentOrder, task("ri", "review-invoice", "READY")], {
        documents: [sent],
        invoice: true,
      }),
      assert: (steps) => {
        expect(steps[3]).toMatchObject({ state: "done", detail: "INV-201 received" });
        expect(steps[4]!.state).toBe("active");
        expect(steps[5]!.state).toBe("waiting");
      },
    },
    {
      name: "a disputed review skips the invoice's approval and fails the outcome",
      record: record("disputed", [approvedOrder, sentOrder, reviewed], { documents: [sent], invoice: true }),
      assert: (steps) => {
        expect(steps[5]!.state).toBe("skipped");
        expect(steps[6]).toMatchObject({ state: "failed", label: "Disputed" });
      },
    },
    {
      name: "an approved order is done throughout",
      record: record("approved", [approvedOrder, sentOrder, reviewed, task("ai", "approve-invoice", "COMPLETED", "bob")], {
        documents: [sent],
        invoice: true,
      }),
      assert: (steps) => {
        expect(steps.map((s) => s.state)).toEqual(["done", "done", "done", "done", "done", "done", "done"]);
        expect(steps[6]!.label).toBe("Approved");
      },
    },
    {
      name: "a rejected invoice fails the outcome",
      record: record("rejected", [approvedOrder, sentOrder, reviewed, task("ai", "approve-invoice", "COMPLETED", "bob")], {
        documents: [sent],
        invoice: true,
      }),
      assert: (steps) => expect(steps[6]).toMatchObject({ state: "failed", label: "Rejected" }),
    },
    {
      name: "a seeded order that started at its invoice's approval skipped the steps it never needed",
      record: record("invoice-approval", [task("ai", "approve-invoice", "READY")], { documents: [sent], invoice: true }),
      assert: (steps) =>
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["approve-order", "skipped"],
          ["purchase-order", "done"],
          ["invoice", "done"],
          ["review-invoice", "skipped"],
          ["approve-invoice", "active"],
          ["outcome", "waiting"],
        ]),
    },
    {
      name: "a task closed without completing fails its step",
      record: record("pending-approval", [task("ao", "approve-order", "EXITED")]),
      assert: (steps) => expect(steps[1]!.state).toBe("failed"),
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => tc.assert(workflowSteps(tc.record)));
  }
});

describe("taskToWork", () => {
  const issuing = record("awaiting-purchase-order", [approvedOrder, task("po", "purchase-order", "READY")]);

  const cases: { name: string; record: OrderRecord; requested?: string; expected: string | undefined }[] = [
    { name: "the task the link names", record: issuing, requested: "ao", expected: "ao" },
    { name: "otherwise the open task", record: issuing, expected: "po" },
    { name: "a link to a task on another order is ignored", record: issuing, requested: "elsewhere", expected: "po" },
    { name: "with nothing open, the latest task", record: record("awaiting-invoice", [approvedOrder, sentOrder]), expected: "po" },
    { name: "no tasks at all", record: record("awaiting-invoice", []), expected: undefined },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(taskToWork(tc.record, tc.requested)).toBe(tc.expected));
  }
});
