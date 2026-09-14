import type { InvoiceRecord, RecordTask } from "./api";

// An invoice's workflow as its page shows it: the order, the review, the
// approval and the outcome. The steps are derived from the record, never
// stored: the order's status is the host's, and each task's status, and
// whether it is terminal, is hmntsk's.

export type StepState = "done" | "active" | "waiting" | "skipped" | "failed";

export type Step = { key: "order" | "review" | "approve" | "outcome"; label: string; state: StepState; detail?: string };

export function workflowSteps(record: InvoiceRecord): Step[] {
  const { order } = record;
  const review = latest(record.tasks, (t) => t.activityKey === "review");
  const approval = latest(record.tasks, (t) => t.activityKey === "approve");

  const orderStep: Step = order
    ? { key: "order", label: "Order placed", state: "done", detail: `${order.id} by ${order.requestedBy}` }
    : { key: "order", label: "Order placed", state: "waiting", detail: "no order on record" };

  // A seeded order may start at approval: its review was never needed.
  const reviewStep =
    !review && order && order.status !== "in-review"
      ? ({ key: "review", label: "Review", state: "skipped", detail: "not needed" } satisfies Step)
      : taskStep("review", "Review", review);

  const approvalStep =
    !approval && order?.status === "disputed"
      ? ({ key: "approve", label: "Approval", state: "skipped", detail: "the review disputed the invoice" } satisfies Step)
      : taskStep("approve", "Approval", approval);

  return [orderStep, reviewStep, approvalStep, outcomeStep(order?.status)];
}

function taskStep(key: "review" | "approve", label: string, task: RecordTask | undefined): Step {
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
      return { key, label, state: "active", detail: "waiting for an approver" };
  }
}

function outcomeStep(status: string | undefined): Step {
  switch (status) {
    case "approved":
      return { key: "outcome", label: "Approved", state: "done", detail: "cleared for payment" };
    case "rejected":
      return { key: "outcome", label: "Rejected", state: "failed", detail: "not paid" };
    case "disputed":
      return { key: "outcome", label: "Disputed", state: "failed", detail: "does not match its order" };
    default:
      return { key: "outcome", label: "Outcome", state: "waiting" };
  }
}

// taskToWork is the task the invoice page works on: the one its link names
// when that task is on this invoice, otherwise the open one, otherwise the
// latest.
export function taskToWork(record: InvoiceRecord, requested?: string): string | undefined {
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
