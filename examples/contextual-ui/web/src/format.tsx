import Chip, { type ChipProps } from "@mui/material/Chip";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";

import type { OrderStatus } from "./api";

const amountFormat = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });

export function formatAmount(amount: number): string {
  return amountFormat.format(amount);
}

// formatBytes is a file size a person reads.
export function formatBytes(size: number): string {
  if (size < 1024) {
    return `${size} B`;
  }

  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KB`;
  }

  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

export const orderStatuses: Record<OrderStatus, { label: string; color: ChipProps["color"] }> = {
  "pending-approval": { label: "Awaiting approval", color: "warning" },
  declined: { label: "Declined", color: "error" },
  "awaiting-purchase-order": { label: "Purchase order due", color: "info" },
  "awaiting-invoice": { label: "Awaiting invoice", color: "info" },
  "invoice-review": { label: "Invoice in review", color: "secondary" },
  "invoice-approval": { label: "Invoice approval", color: "warning" },
  disputed: { label: "Disputed", color: "error" },
  approved: { label: "Approved", color: "success" },
  rejected: { label: "Rejected", color: "error" },
};

// OrderStatusChip shows where the host's order has got to, which the workflow
// decides from the order's completed tasks.
export function OrderStatusChip({ status }: { status: OrderStatus }) {
  const { label, color } = orderStatuses[status] ?? { label: status, color: "default" };

  return <Chip size="small" variant="outlined" label={label} color={color} />;
}

// Due is a deadline relative to now, in red once it has passed.
export function Due({ at }: { at?: string }) {
  if (!at) {
    return (
      <Typography variant="body2" color="text.secondary" component="span">
        none
      </Typography>
    );
  }

  const due = new Date(at);
  const overdue = due.getTime() < Date.now();

  return (
    <Tooltip title={due.toLocaleString()}>
      <Typography
        variant="body2"
        component="span"
        color={overdue ? "error" : "text.primary"}
        sx={{ fontWeight: overdue ? 700 : 400 }}
      >
        {relative(due)}
      </Typography>
    </Tooltip>
  );
}

const relativeFormat = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function relative(date: Date): string {
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);

  if (Math.abs(seconds) < 60) {
    return relativeFormat.format(seconds, "second");
  }

  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) {
    return relativeFormat.format(minutes, "minute");
  }

  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) {
    return relativeFormat.format(hours, "hour");
  }

  return relativeFormat.format(Math.round(hours / 24), "day");
}
