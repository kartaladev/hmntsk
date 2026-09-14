import Chip, { type ChipProps } from "@mui/material/Chip";

const colors: Record<string, ChipProps["color"]> = {
  READY: "info",
  RESERVED: "warning",
  IN_PROGRESS: "secondary",
  COMPLETED: "success",
  FAILED: "error",
  ERROR: "error",
};

// StatusChip shows a task's lifecycle status. It says where the task got to,
// never what was decided: a rejected invoice is COMPLETED too.
export function StatusChip({ status }: { status: string }) {
  return <Chip size="small" label={status.replace("_", " ")} color={colors[status] ?? "default"} />;
}
