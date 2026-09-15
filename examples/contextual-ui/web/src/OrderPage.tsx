import DownloadIcon from "@mui/icons-material/Download";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Breadcrumbs from "@mui/material/Breadcrumbs";
import Chip from "@mui/material/Chip";
import CircularProgress from "@mui/material/CircularProgress";
import Link from "@mui/material/Link";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Step from "@mui/material/Step";
import StepLabel from "@mui/material/StepLabel";
import Stepper from "@mui/material/Stepper";
import Typography from "@mui/material/Typography";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { type ReactNode, useEffect, useState } from "react";

import { ApiError, api, documentPath, type DemoUser, type OrderDocument, type OrderRecord, type RecordTask, type TaskType } from "./api";
import { AppLink } from "./AppLink";
import { Due, formatAmount, formatBytes, OrderStatusChip } from "./format";
import { contextLink } from "./links";
import { navigate, orderPath } from "./pages";
import { StatusChip } from "./StatusChip";
import { settled, taskToWork, workflowSteps } from "./steps";
import { TaskPanel } from "./TaskPanel";
import { useTicker } from "./useTicker";

type Props = {
  orderId: string;
  taskId?: string;
  user: DemoUser;
  types: Record<string, TaskType>;
  revision: number;
  onChange: () => void;
};

const documentColumns: GridColDef<OrderDocument>[] = [
  {
    field: "fileName",
    headerName: "Document",
    flex: 1,
    minWidth: 180,
    renderCell: ({ row }) => (
      // A download, served as an attachment: the browser never renders it in the page's origin.
      <Link href={documentPath(row.id)} underline="hover" sx={{ display: "inline-flex", alignItems: "center", gap: 0.5 }}>
        <DownloadIcon fontSize="inherit" />
        {row.fileName}
      </Link>
    ),
  },
  {
    field: "method",
    headerName: "How",
    flex: 1,
    minWidth: 200,
    valueGetter: (_value, row) => (row.method === "sent" ? `Sent to ${row.sentTo}` : "Uploaded"),
  },
  { field: "createdBy", headerName: "By", width: 90 },
  { field: "size", headerName: "Size", width: 90, type: "number", valueFormatter: (value: number) => formatBytes(value) },
  { field: "createdAt", headerName: "When", type: "dateTime", width: 170, valueGetter: (_value, row) => new Date(row.createdAt) },
];

// OrderPage is the business record with its work inside it: the order, its
// purchase order and invoice, every step of its workflow, and the task the
// viewer is here to do. The task types' hmntsk.route links every task here.
// The page is keyed by order, so it never shows one order's record under
// another.
export function OrderPage({ orderId, taskId, user, types, revision, onChange }: Props) {
  const [record, setRecord] = useState<OrderRecord>();
  const [problem, setProblem] = useState<string>();

  // The workflow creates each next task in the relay, a moment after the page's
  // own request returns, and the supplier's invoice arrives on its own time;
  // re-reading every few seconds shows them arrive, until the order is settled.
  const tick = useTicker(record && settled(record.order.status) ? null : 3000);

  useEffect(() => {
    let cancelled = false;

    api.order(orderId).then(
      (r) => {
        if (!cancelled) {
          setRecord(r);
          setProblem(undefined);
        }
      },
      (e: ApiError) => !cancelled && setProblem(e.status === 404 ? `There is no order ${orderId}.` : e.message),
    );

    return () => {
      cancelled = true;
    };
  }, [orderId, revision, tick]);

  if (problem && !record) {
    return <Alert severity="error">{problem}</Alert>;
  }

  if (!record) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", p: 6 }}>
        <CircularProgress />
      </Box>
    );
  }

  const { order, invoice } = record;
  const working = taskToWork(record, taskId);
  const steps = workflowSteps(record);
  const activeStep = steps.findIndex((s) => s.state === "active" || s.state === "waiting");

  // Each task links through its type's hmntsk.route, like the inbox and the notifications do.
  const linkOf = (task: RecordTask) =>
    contextLink({ id: task.id, type: task.type, correlation: { ownerRef: order.id, activityKey: task.activityKey } }, types[task.type]) ??
    orderPath(order.id);

  const taskColumns: GridColDef<RecordTask>[] = [
    { field: "type", headerName: "Task", flex: 1, minWidth: 170, valueGetter: (_value, row) => types[row.type]?.title ?? row.type },
    { field: "status", headerName: "Status", width: 130, renderCell: ({ row }) => <StatusChip status={row.status} /> },
    { field: "assignee", headerName: "Held by", width: 100, valueGetter: (_value, row) => row.assignee ?? "the pool" },
    {
      field: "dueAt",
      headerName: "Due",
      width: 130,
      renderCell: ({ row }) => (row.terminal ? "" : <Due at={row.dueAt} />),
    },
    { field: "createdAt", headerName: "Created", type: "dateTime", width: 170, valueGetter: (_value, row) => new Date(row.createdAt) },
  ];

  return (
    <Stack spacing={3}>
      <Box>
        <Breadcrumbs aria-label="Breadcrumb" sx={{ mb: 1 }}>
          <AppLink href="/orders" underline="hover" color="inherit">
            Orders
          </AppLink>
          <Typography color="text.primary">{order.id}</Typography>
        </Breadcrumbs>
        <Stack direction="row" spacing={2} sx={{ alignItems: "center", flexWrap: "wrap" }}>
          <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
            Order {order.id}
          </Typography>
          <OrderStatusChip status={order.status} />
        </Stack>
        <Typography color="text.secondary">
          {order.description} · {order.supplier} · {formatAmount(order.amount)}
        </Typography>
      </Box>

      <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", lg: "minmax(0, 1fr) 460px" }, alignItems: "start" }}>
        <Stack spacing={3} sx={{ minWidth: 0 }}>
          <Paper sx={{ p: 3 }}>
            <Section title="Details" />
            <Box component="dl" sx={{ display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 3, rowGap: 1, m: 0 }}>
              <Detail
                label="Supplier"
                value={
                  <Stack direction="row" spacing={1} sx={{ alignItems: "center", flexWrap: "wrap" }}>
                    <span>{order.supplier}</span>
                    <Chip
                      size="small"
                      variant="outlined"
                      label={order.supplierRegistered ? "on the supplier registry" : "not on the supplier registry"}
                      color={order.supplierRegistered ? "primary" : "default"}
                    />
                  </Stack>
                }
              />
              <Detail label="Amount" value={formatAmount(order.amount)} />
              <Detail label="What is being bought" value={order.description} />
              <Detail label="Requested by" value={order.requestedBy} />
              <Detail label="Placed" value={new Date(order.createdAt).toLocaleString()} />
            </Box>
          </Paper>

          <Paper sx={{ p: 3 }}>
            <Section title="Workflow" />
            <Stepper activeStep={activeStep < 0 ? steps.length : activeStep} orientation="vertical">
              {steps.map((step) => (
                <Step key={step.key} completed={step.state === "done"}>
                  <StepLabel
                    error={step.state === "failed"}
                    optional={
                      step.detail && (
                        <Typography variant="caption" color={step.state === "failed" ? "error" : "text.secondary"}>
                          {step.detail}
                        </Typography>
                      )
                    }
                  >
                    {step.label}
                    {step.state === "skipped" && " (skipped)"}
                  </StepLabel>
                </Step>
              ))}
            </Stepper>
          </Paper>

          <Paper sx={{ overflow: "hidden" }}>
            <Section title="Purchase order" sx={{ px: 3, pt: 2 }} />
            <DataGrid
              aria-label="Purchase order documents"
              rows={record.documents}
              columns={documentColumns}
              hideFooter
              disableColumnMenu
              localeText={{
                noRowsLabel: order.supplierRegistered
                  ? "Not sent yet: purchasing sends it once the order is approved."
                  : "Not uploaded yet: purchasing uploads it once the order is approved.",
              }}
            />
          </Paper>

          <Paper sx={{ p: 3 }}>
            <Section title="Invoice" />
            {invoice ? (
              <Box component="dl" sx={{ display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 3, rowGap: 1, m: 0 }}>
                <Detail label="Invoice" value={invoice.id} />
                <Detail label="Amount" value={formatAmount(invoice.amount)} />
                <Detail label="Received" value={new Date(invoice.receivedAt).toLocaleString()} />
              </Box>
            ) : order.status === "awaiting-invoice" && order.invoiceExpectedAt ? (
              <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
                <CircularProgress size={16} />
                <Typography variant="body2" color="text.secondary">
                  The supplier has the purchase order. Their invoice is expected <Due at={order.invoiceExpectedAt} />.
                </Typography>
              </Stack>
            ) : (
              <Typography variant="body2" color="text.secondary">
                {order.status === "declined"
                  ? "None: the order was declined."
                  : "The supplier invoices once they have the purchase order."}
              </Typography>
            )}
          </Paper>

          <Paper sx={{ overflow: "hidden" }}>
            <Section title="Tasks on this order" sx={{ px: 3, pt: 2 }} />
            <DataGrid
              aria-label="Tasks on this order"
              rows={record.tasks}
              columns={taskColumns}
              hideFooter
              disableColumnMenu
              onRowClick={({ row }) => navigate(linkOf(row))}
              getRowClassName={({ id }) => (id === working ? "working" : "")}
              localeText={{ noRowsLabel: "No tasks yet." }}
              sx={{
                "& .MuiDataGrid-row": { cursor: "pointer" },
                "& .MuiDataGrid-row.working": { bgcolor: "action.selected" },
              }}
            />
          </Paper>
        </Stack>

        <Paper component="section" aria-label="Your task">
          {working ? (
            // Keyed by task only: the panel keeps the task its own actions returned, and unsaved form values.
            <TaskPanel key={working} id={working} user={user.id} types={types} onChange={onChange} />
          ) : (
            <Typography color="text.secondary" sx={{ p: 3 }}>
              {order.status === "awaiting-invoice"
                ? "Nothing to do until the supplier's invoice arrives."
                : "There is no task on this order."}
            </Typography>
          )}
        </Paper>
      </Box>
    </Stack>
  );
}

function Section({ title, sx }: { title: string; sx?: object }) {
  return (
    <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, mb: 2, ...sx }}>
      {title}
    </Typography>
  );
}

function Detail({ label, value }: { label: string; value: ReactNode }) {
  return (
    <>
      <Typography component="dt" variant="body2" color="text.secondary">
        {label}
      </Typography>
      <Typography component="dd" variant="body2" sx={{ m: 0 }}>
        {value}
      </Typography>
    </>
  );
}
