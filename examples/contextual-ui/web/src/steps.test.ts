import { describe, expect, it } from "vitest";

import type { InvoiceRecord, Order, RecordTask } from "./api";
import { taskToWork, workflowSteps, type Step } from "./steps";

const order = (status: Order["status"]): Order => ({
  id: "ORD-201",
  invoiceId: "INV-201",
  supplier: "Stark Industries",
  description: "Laptops",
  amount: 4200,
  requestedBy: "erin",
  status,
  createdAt: "2026-09-14T09:00:00Z",
});

// The server says whether a task is terminal; these are the statuses hmntsk
// calls terminal, for building fixtures.
const terminal = ["COMPLETED", "FAILED", "ERROR", "EXITED", "OBSOLETE"];

const task = (id: string, activityKey: string, status: string, assignee?: string): RecordTask => ({
  id,
  type: `invoice.${activityKey}`,
  status,
  terminal: terminal.includes(status),
  assignee,
  activityKey,
  createdAt: "2026-09-14T09:00:00Z",
});

const record = (status: Order["status"] | null, ...tasks: RecordTask[]): InvoiceRecord => ({
  invoice: { id: "INV-201", supplier: "Stark Industries", amount: 4200 },
  order: status ? order(status) : null,
  tasks,
});

// Only the states, which is what a reader of the stepper sees first.
const states = (steps: Step[]) => steps.map((s) => [s.key, s.state]);

describe("workflowSteps", () => {
  const cases: { name: string; record: InvoiceRecord; assert: (steps: Step[]) => void }[] = [
    {
      name: "a placed order waits at a review nobody holds",
      record: record("in-review", task("r", "review", "READY")),
      assert: (steps) => {
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["review", "active"],
          ["approve", "waiting"],
          ["outcome", "waiting"],
        ]);
        expect(steps[0]!.detail).toBe("ORD-201 by erin");
        expect(steps[1]!.detail).toBe("waiting for an approver");
      },
    },
    {
      name: "a held review names who holds it",
      record: record("in-review", task("r", "review", "IN_PROGRESS", "alice")),
      assert: (steps) => expect(steps[1]!.detail).toBe("alice is working on it"),
    },
    {
      name: "a matching review hands over to approval",
      record: record("awaiting-approval", task("r", "review", "COMPLETED", "alice"), task("a", "approve", "RESERVED", "bob")),
      assert: (steps) => {
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["review", "done"],
          ["approve", "active"],
          ["outcome", "waiting"],
        ]);
        expect(steps[1]!.detail).toBe("by alice");
        expect(steps[2]!.detail).toBe("bob has claimed it");
      },
    },
    {
      name: "a disputed review skips approval and fails the outcome",
      record: record("disputed", task("r", "review", "COMPLETED", "alice")),
      assert: (steps) =>
        expect(states(steps)).toEqual([
          ["order", "done"],
          ["review", "done"],
          ["approve", "skipped"],
          ["outcome", "failed"],
        ]),
    },
    {
      name: "an approved order is done throughout",
      record: record("approved", task("r", "review", "COMPLETED", "alice"), task("a", "approve", "COMPLETED", "bob")),
      assert: (steps) => {
        expect(states(steps).map(([, state]) => state)).toEqual(["done", "done", "done", "done"]);
        expect(steps[3]!.label).toBe("Approved");
      },
    },
    {
      name: "a rejected order fails the outcome",
      record: record("rejected", task("r", "review", "COMPLETED", "alice"), task("a", "approve", "COMPLETED", "bob")),
      assert: (steps) => {
        expect(steps[3]!.state).toBe("failed");
        expect(steps[3]!.label).toBe("Rejected");
      },
    },
    {
      name: "an order that started at approval skipped review",
      record: record("awaiting-approval", task("a", "approve", "READY")),
      assert: (steps) => expect(steps[1]!.state).toBe("skipped"),
    },
    {
      name: "an invoice with no order has no order step to finish",
      record: record(null, task("a", "approve", "READY")),
      assert: (steps) => expect(steps[0]!.state).toBe("waiting"),
    },
    {
      name: "a task closed without completing fails its step",
      record: record("in-review", task("r", "review", "EXITED")),
      assert: (steps) => expect(steps[1]!.state).toBe("failed"),
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => tc.assert(workflowSteps(tc.record)));
  }
});

describe("taskToWork", () => {
  const reviewed = record(
    "awaiting-approval",
    task("r", "review", "COMPLETED", "alice"),
    task("a", "approve", "READY"),
  );

  const cases: { name: string; record: InvoiceRecord; requested?: string; expected: string | undefined }[] = [
    { name: "the task the link names", record: reviewed, requested: "r", expected: "r" },
    { name: "otherwise the open task", record: reviewed, expected: "a" },
    { name: "a link to a task on another invoice is ignored", record: reviewed, requested: "elsewhere", expected: "a" },
    {
      name: "with nothing open, the latest task",
      record: record("approved", task("r", "review", "COMPLETED"), task("a", "approve", "COMPLETED")),
      expected: "a",
    },
    { name: "no tasks at all", record: record(null), expected: undefined },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(taskToWork(tc.record, tc.requested)).toBe(tc.expected));
  }
});
