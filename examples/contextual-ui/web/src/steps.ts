import type { OrderRecord, OrderStatus, RecordTask } from "./api";

// An order's workflow as its page shows it: the order, its approval, the
// purchase order, the supplier's invoice, the invoice's review and approval,
// and the outcome. The steps are derived from the record, never stored: the
// order's status and documents are the host's, and each task's status, and
// whether it is terminal, is hmntsk's.

export type StepState = "done" | "active" | "waiting" | "skipped" | "failed";

export type StepKey =
  | "order"
  | "approve-order"
  | "purchase-order"
  | "invoice"
  | "review-invoice"
  | "approve-invoice"
  | "outcome";

export type Step = { key: StepKey; label: string; state: StepState; detail?: string };

// Where each status sits in the workflow, for telling a step that is still
// ahead from one the order started past. The order's end states come last.
const progress: Record<OrderStatus, number> = {
  "pending-approval": 0,
  "awaiting-purchase-order": 1,
  "awaiting-invoice": 2,
  "invoice-review": 3,
  "invoice-approval": 4,
  declined: 5,
  disputed: 5,
  approved: 5,
  rejected: 5,
};

export function workflowSteps(record: OrderRecord): Step[] {
  const { order, invoice, documents } = record;
  const status = order.status;
  const declined = status === "declined";
  const at = progress[status] ?? 0;

  const byActivity = (activity: string) => latest(record.tasks, (t) => t.activityKey === activity);

  // A step with no task behind it was either never needed, because a seeded
  // order started past it, or is still ahead.
  const notNeeded = (key: StepKey, label: string, reachedAt: number): Step | undefined =>
    at > reachedAt ? { key, label, state: "skipped", detail: "not needed" } : undefined;

  const skippedBecause = (key: StepKey, label: string, detail: string): Step => ({ key, label, state: "skipped", detail });

  const orderStep: Step = { key: "order", label: "Order placed", state: "done", detail: `${order.id} by ${order.requestedBy}` };

  const approval = byActivity("approve-order");
  const approvalStep = approval
    ? taskStep("approve-order", "Order approval", approval)
    : (notNeeded("approve-order", "Order approval", 0) ?? taskStep("approve-order", "Order approval", undefined));

  const purchaseOrder = documents.at(-1);
  const purchaseOrderStep: Step = declined
    ? skippedBecause("purchase-order", "Purchase order", "the order was declined")
    : purchaseOrder
      ? {
          key: "purchase-order",
          label: "Purchase order",
          state: "done",
          detail: purchaseOrder.method === "sent" ? `sent to ${purchaseOrder.sentTo}` : `uploaded by ${purchaseOrder.createdBy}`,
        }
      : taskStep("purchase-order", "Purchase order", byActivity("purchase-order"));

  const invoiceStep: Step = declined
    ? skippedBecause("invoice", "Invoice", "the order was declined")
    : invoice
      ? { key: "invoice", label: "Invoice", state: "done", detail: `${invoice.id} received` }
      : status === "awaiting-invoice"
        ? { key: "invoice", label: "Invoice", state: "active", detail: "the supplier has the purchase order" }
        : { key: "invoice", label: "Invoice", state: "waiting" };

  const review = byActivity("review-invoice");
  const reviewStep: Step = declined
    ? skippedBecause("review-invoice", "Invoice review", "the order was declined")
    : review
      ? taskStep("review-invoice", "Invoice review", review)
      : (notNeeded("review-invoice", "Invoice review", 3) ?? taskStep("review-invoice", "Invoice review", undefined));

  const invoiceApproval = byActivity("approve-invoice");
  const invoiceApprovalStep: Step = declined
    ? skippedBecause("approve-invoice", "Invoice approval", "the order was declined")
    : !invoiceApproval && status === "disputed"
      ? skippedBecause("approve-invoice", "Invoice approval", "the review disputed the invoice")
      : taskStep("approve-invoice", "Invoice approval", invoiceApproval);

  return [orderStep, approvalStep, purchaseOrderStep, invoiceStep, reviewStep, invoiceApprovalStep, outcomeStep(status)];
}

function taskStep(key: StepKey, label: string, task: RecordTask | undefined): Step {
  if (!task) {
    return { key, label, state: "waiting" };
  }

  if (task.status === "COMPLETED") {
    return { key, label, state: "done", detail: task.assignee ? `by ${task.assignee}` : undefined };
  }

  if (task.terminal) {
    return { key, label, state: "failed", detail: task.status.toLowerCase() };
  }

  switch (task.status) {
    case "IN_PROGRESS":
      return { key, label, state: "active", detail: `${task.assignee} is working on it` };
    case "RESERVED":
      return { key, label, state: "active", detail: `${task.assignee} has claimed it` };
    case "SUSPENDED":
      return { key, label, state: "active", detail: "suspended" };
    default:
      return { key, label, state: "active", detail: "waiting to be claimed" };
  }
}

function outcomeStep(status: OrderStatus): Step {
  switch (status) {
    case "approved":
      return { key: "outcome", label: "Approved", state: "done", detail: "cleared for payment" };
    case "rejected":
      return { key: "outcome", label: "Rejected", state: "failed", detail: "not paid" };
    case "disputed":
      return { key: "outcome", label: "Disputed", state: "failed", detail: "does not match its order" };
    case "declined":
      return { key: "outcome", label: "Declined", state: "failed", detail: "nothing was ordered" };
    default:
      return { key: "outcome", label: "Outcome", state: "waiting" };
  }
}

// settled reports an order nothing more will happen to.
export function settled(status: OrderStatus): boolean {
  return progress[status] === 5;
}

// taskToWork is the task the order page works on: the one its link names when
// that task is on this order, otherwise the open one, otherwise the latest.
export function taskToWork(record: OrderRecord, requested?: string): string | undefined {
  if (requested && record.tasks.some((t) => t.id === requested)) {
    return requested;
  }

  return (latest(record.tasks, (t) => !t.terminal) ?? record.tasks.at(-1))?.id;
}

// latest is the last task matching, the record listing them oldest first.
function latest(tasks: RecordTask[], matches: (task: RecordTask) => boolean): RecordTask | undefined {
  for (let i = tasks.length - 1; i >= 0; i--) {
    if (matches(tasks[i]!)) {
      return tasks[i];
    }
  }

  return undefined;
}
