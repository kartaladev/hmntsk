import Chip, { type ChipProps } from "@mui/material/Chip";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";

import type { OrderStatus } from "./api";

const amountFormat = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });

export function formatAmount(amount: number): string {
  return amountFormat.format(amount);
}

const orderStatuses: Record<OrderStatus, { label: string; color: ChipProps["color"] }> = {
  "in-review": { label: "In review", color: "info" },
  "awaiting-approval": { label: "Awaiting approval", color: "warning" },
  disputed: { label: "Disputed", color: "error" },
  approved: { label: "Approved", color: "success" },
  rejected: { label: "Rejected", color: "error" },
};

// OrderStatusChip shows where the host's order has got to, which the workflow
// decides from the invoice's completed tasks.
export function OrderStatusChip({ status }: { status: OrderStatus }) {
  const { label, color } = orderStatuses[status] ?? { label: status, color: "default" };

  return <Chip size="small" variant="outlined" label={label} color={color} />;
}

// Due is a deadline relative to now, in red once it has passed.
export function Due({ at }: { at?: string }) {
  if (!at) {
    return (
      <Typography variant="body2" color="text.secondary">
        none
      </Typography>
    );
  }

  const due = new Date(at);
  const overdue = due.getTime() < Date.now();

  return (
    <Tooltip title={due.toLocaleString()}>
      <Typography variant="body2" color={overdue ? "error" : "text.primary"} sx={{ fontWeight: overdue ? 700 : 400 }}>
        {relative(due)}
      </Typography>
    </Tooltip>
  );
}

const relativeFormat = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

function relative(date: Date): string {
  const minutes = Math.round((date.getTime() - Date.now()) / 60_000);

  if (Math.abs(minutes) < 60) {
    return relativeFormat.format(minutes, "minute");
  }

  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) {
    return relativeFormat.format(hours, "hour");
  }

  return relativeFormat.format(Math.round(hours / 24), "day");
}
