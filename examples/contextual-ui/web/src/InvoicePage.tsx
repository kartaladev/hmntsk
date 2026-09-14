import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Breadcrumbs from "@mui/material/Breadcrumbs";
import CircularProgress from "@mui/material/CircularProgress";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Step from "@mui/material/Step";
import StepLabel from "@mui/material/StepLabel";
import Stepper from "@mui/material/Stepper";
import Typography from "@mui/material/Typography";
import { useEffect, useState } from "react";

import { ApiError, api, type DemoUser, type InvoiceRecord, type TaskType } from "./api";
import { AppLink } from "./AppLink";
import { formatAmount, OrderStatusChip } from "./format";
import { contextLink } from "./links";
import { invoicePath, navigate } from "./pages";
import { StatusChip } from "./StatusChip";
import { taskToWork, workflowSteps } from "./steps";
import { TaskPanel } from "./TaskPanel";
import { useTicker } from "./useTicker";

type Props = {
  invoiceId: string;
  taskId?: string;
  user: DemoUser;
  types: Record<string, TaskType>;
  revision: number;
  onChange: () => void;
};

// An order in one of these has nothing left to happen to it.
const settled = new Set(["approved", "rejected", "disputed"]);

// InvoicePage is the business record with its work inside it: the invoice and
// its order, every step of its review and approval, and the task the viewer
// is here to do. The task types' hmntsk.route links every task here. The page
// is keyed by invoice, so it never shows one invoice's record under another.
export function InvoicePage({ invoiceId, taskId, user, types, revision, onChange }: Props) {
  const [record, setRecord] = useState<InvoiceRecord>();
  const [problem, setProblem] = useState<string>();

  // The workflow creates the approval after a review completes, in the relay,
  // a moment after the page's own request returns; re-reading every few
  // seconds shows it arrive, until the order is settled.
  const tick = useTicker(record?.order && settled.has(record.order.status) ? null : 3000);

  useEffect(() => {
    let cancelled = false;

    api.invoice(invoiceId).then(
      (r) => {
        if (!cancelled) {
          setRecord(r);
          setProblem(undefined);
        }
      },
      (e: ApiError) => !cancelled && setProblem(e.status === 404 ? `There is no invoice ${invoiceId}.` : e.message),
    );

    return () => {
      cancelled = true;
    };
  }, [invoiceId, revision, tick]);

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

  const { invoice, order } = record;
  const working = taskToWork(record, taskId);
  const steps = workflowSteps(record);
  const activeStep = steps.findIndex((s) => s.state === "active" || s.state === "waiting");

  return (
    <Stack spacing={3}>
      <Box>
        <Breadcrumbs aria-label="Breadcrumb" sx={{ mb: 1 }}>
          <AppLink href="/orders" underline="hover" color="inherit">
            Orders
          </AppLink>
          <Typography color="text.primary">{invoice.id}</Typography>
        </Breadcrumbs>
        <Stack direction="row" spacing={2} sx={{ alignItems: "center", flexWrap: "wrap" }}>
          <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
            Invoice {invoice.id}
          </Typography>
          {order && <OrderStatusChip status={order.status} />}
        </Stack>
        <Typography color="text.secondary">
          {invoice.supplier} · {formatAmount(invoice.amount)}
        </Typography>
      </Box>

      <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", lg: "minmax(0, 1fr) 460px" }, alignItems: "start" }}>
        <Stack spacing={3}>
          <Paper sx={{ p: 3 }}>
            <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, mb: 2 }}>
              Details
            </Typography>
            <Box
              component="dl"
              sx={{ display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 3, rowGap: 1, m: 0 }}
            >
              <Detail label="Supplier" value={invoice.supplier} />
              <Detail label="Amount" value={formatAmount(invoice.amount)} />
              <Detail label="Order" value={order?.id ?? "none on record"} />
              {order && (
                <>
                  <Detail label="What was bought" value={order.description} />
                  <Detail label="Requested by" value={order.requestedBy} />
                  <Detail label="Placed" value={new Date(order.createdAt).toLocaleString()} />
                </>
              )}
            </Box>
          </Paper>

          <Paper sx={{ p: 3 }}>
            <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, mb: 2 }}>
              Workflow
            </Typography>
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
            <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, px: 3, pt: 2, pb: 1 }}>
              Tasks on this invoice
            </Typography>
            <List aria-label="Tasks on this invoice">
              {record.tasks.map((task) => (
                <ListItemButton
                  key={task.id}
                  selected={task.id === working}
                  // Each task links through its type's hmntsk.route, like the inbox and the notifications do.
                  onClick={() =>
                    navigate(
                      contextLink(
                        { id: task.id, type: task.type, correlation: { ownerRef: invoice.id, activityKey: task.activityKey } },
                        types[task.type],
                      ) ?? invoicePath(invoice.id),
                    )
                  }
                  sx={{ px: 3 }}
                >
                  <ListItemText
                    primary={types[task.type]?.title ?? task.type}
                    secondary={task.assignee ? `held by ${task.assignee}` : "in the pool"}
                  />
                  <StatusChip status={task.status} />
                </ListItemButton>
              ))}
            </List>
            {record.tasks.length === 0 && (
              <Typography color="text.secondary" sx={{ px: 3, pb: 2 }}>
                No tasks yet.
              </Typography>
            )}
          </Paper>
        </Stack>

        <Paper component="section" aria-label="Your task">
          {working ? (
            // Keyed by task only: the panel keeps the task its own actions returned, and unsaved form values.
            <TaskPanel key={working} id={working} user={user.id} types={types} onChange={onChange} />
          ) : (
            <Typography color="text.secondary" sx={{ p: 3 }}>
              There is no task on this invoice.
            </Typography>
          )}
        </Paper>
      </Box>
    </Stack>
  );
}

function Detail({ label, value }: { label: string; value: string }) {
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
