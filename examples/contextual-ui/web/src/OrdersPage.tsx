import AddShoppingCartIcon from "@mui/icons-material/AddShoppingCart";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import InputAdornment from "@mui/material/InputAdornment";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";
import { useEffect, useState } from "react";

import { api, type DemoUser, type Order } from "./api";
import { AppLink } from "./AppLink";
import { formatAmount, OrderStatusChip } from "./format";
import { invoicePath, navigate } from "./pages";
import { useTicker } from "./useTicker";

type Props = {
  user: DemoUser;
};

const emptyForm = { supplier: "", description: "", amount: "" };

// OrdersPage is where work starts. Placing an order creates its invoice and
// the invoice's review task in one transaction on the server, and every
// approver is notified.
export function OrdersPage({ user }: Props) {
  const [orders, setOrders] = useState<Order[]>();
  const [form, setForm] = useState(emptyForm);
  const [placed, setPlaced] = useState<Order>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  // Order statuses move as approvers work, and nobody notifies purchasing of
  // that, so the list is re-read every few seconds.
  const tick = useTicker(4000);
  const purchasing = user.groups.includes("purchasing");

  useEffect(() => {
    api.orders().then(setOrders, (e: Error) => setError(e.message));
  }, [tick]);

  const amount = Number(form.amount);
  const valid = form.supplier.trim() !== "" && form.description.trim() !== "" && Number.isInteger(amount) && amount > 0;

  const place = async () => {
    setBusy(true);
    setError(undefined);

    try {
      const order = await api.placeOrder({ supplier: form.supplier, description: form.description, amount });
      setPlaced(order);
      setOrders((current) => [order, ...(current ?? [])]);
      setForm(emptyForm);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Stack spacing={3}>
      <Box>
        <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
          Orders
        </Typography>
        <Typography color="text.secondary">
          Every purchase, and where its invoice has got to in review and approval.
        </Typography>
      </Box>

      {error && (
        <Alert severity="error" onClose={() => setError(undefined)}>
          {error}
        </Alert>
      )}

      <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", lg: "360px 1fr" }, alignItems: "start" }}>
        <Paper sx={{ p: 3 }}>
          <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, mb: 2 }}>
            Place an order
          </Typography>

          {purchasing ? (
            <Stack
              component="form"
              spacing={2}
              noValidate
              onSubmit={(e) => {
                e.preventDefault();
                if (valid) {
                  void place();
                }
              }}
            >
              <TextField
                label="Supplier"
                required
                value={form.supplier}
                onChange={(e) => setForm({ ...form, supplier: e.target.value })}
              />
              <TextField
                label="What is being bought"
                required
                multiline
                minRows={2}
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
              <TextField
                label="Amount"
                required
                type="number"
                value={form.amount}
                onChange={(e) => setForm({ ...form, amount: e.target.value })}
                slotProps={{
                  input: { startAdornment: <InputAdornment position="start">$</InputAdornment> },
                  htmlInput: { min: 1, step: 1 },
                }}
              />
              <Button type="submit" variant="contained" startIcon={<AddShoppingCartIcon />} disabled={!valid || busy}>
                Place order
              </Button>
              <Typography variant="body2" color="text.secondary">
                The supplier&apos;s invoice arrives with the order, and billing asks an approver to review it.
              </Typography>
            </Stack>
          ) : (
            <Alert severity="info">
              Only purchasing places orders. Sign in as erin to try it; as {user.name} you review and approve their
              invoices.
            </Alert>
          )}

          {placed && (
            <Alert severity="success" sx={{ mt: 2 }} onClose={() => setPlaced(undefined)}>
              {placed.id} placed. Its invoice{" "}
              <AppLink href={invoicePath(placed.invoiceId)}>{placed.invoiceId}</AppLink> is waiting for review.
            </Alert>
          )}
        </Paper>

        <Paper sx={{ overflow: "hidden" }}>
          <TableContainer>
            <Table size="small" aria-label="Orders">
              <TableHead>
                <TableRow>
                  <TableCell>Order</TableCell>
                  <TableCell>Description</TableCell>
                  <TableCell>Supplier</TableCell>
                  <TableCell align="right">Amount</TableCell>
                  <TableCell>Invoice</TableCell>
                  <TableCell>Status</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {(orders ?? []).map((order) => (
                  <TableRow
                    key={order.id}
                    hover
                    onClick={() => navigate(invoicePath(order.invoiceId))}
                    sx={{ cursor: "pointer" }}
                  >
                    <TableCell sx={{ fontWeight: 600, whiteSpace: "nowrap" }}>{order.id}</TableCell>
                    <TableCell>{order.description}</TableCell>
                    <TableCell>{order.supplier}</TableCell>
                    <TableCell align="right">{formatAmount(order.amount)}</TableCell>
                    <TableCell>
                      <AppLink
                        href={invoicePath(order.invoiceId)}
                        underline="hover"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {order.invoiceId}
                      </AppLink>
                    </TableCell>
                    <TableCell>
                      <OrderStatusChip status={order.status} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
          {orders?.length === 0 && (
            <Typography color="text.secondary" sx={{ p: 4, textAlign: "center" }}>
              No orders yet.
            </Typography>
          )}
        </Paper>
      </Box>
    </Stack>
  );
}
