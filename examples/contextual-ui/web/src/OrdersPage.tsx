import AddShoppingCartIcon from "@mui/icons-material/AddShoppingCart";
import VerifiedIcon from "@mui/icons-material/Verified";
import Alert from "@mui/material/Alert";
import Autocomplete from "@mui/material/Autocomplete";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import InputAdornment from "@mui/material/InputAdornment";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useEffect, useState } from "react";

import { api, type DemoUser, type Order, type Supplier } from "./api";
import { AppLink } from "./AppLink";
import { formatAmount, orderStatuses, OrderStatusChip } from "./format";
import { navigate, orderPath } from "./pages";
import { registryEntry } from "./purchaseOrder";
import { useTicker } from "./useTicker";

type Props = {
  user: DemoUser;
};

const emptyForm = { supplier: "", description: "", amount: "" };

const columns: GridColDef<Order>[] = [
  {
    field: "id",
    headerName: "Order",
    width: 110,
    renderCell: ({ row }) => (
      <AppLink href={orderPath(row.id)} underline="hover" onClick={(e) => e.stopPropagation()}>
        {row.id}
      </AppLink>
    ),
  },
  { field: "description", headerName: "Description", flex: 1, minWidth: 200 },
  {
    field: "supplier",
    headerName: "Supplier",
    flex: 0.6,
    minWidth: 160,
    renderCell: ({ row }) => (
      <Stack direction="row" spacing={0.5} sx={{ alignItems: "center" }}>
        <span>{row.supplier}</span>
        {row.supplierRegistered && (
          <Tooltip title="On the supplier registry">
            <VerifiedIcon fontSize="inherit" color="primary" aria-label="On the supplier registry" />
          </Tooltip>
        )}
      </Stack>
    ),
  },
  {
    field: "amount",
    headerName: "Amount",
    type: "number",
    width: 110,
    valueFormatter: (value: number) => formatAmount(value),
  },
  { field: "invoiceId", headerName: "Invoice", width: 100, valueGetter: (_value, row) => row.invoiceId ?? "" },
  {
    field: "status",
    headerName: "Status",
    width: 170,
    type: "singleSelect",
    valueOptions: Object.entries(orderStatuses).map(([value, { label }]) => ({ value, label })),
    renderCell: ({ row }) => <OrderStatusChip status={row.status} />,
  },
  {
    field: "createdAt",
    headerName: "Placed",
    type: "dateTime",
    width: 170,
    valueGetter: (_value, row) => new Date(row.createdAt),
  },
];

// OrdersPage is where work starts. Placing an order creates it with its
// approval task in one transaction on the server, and the budget holder is
// notified. The orders are a data grid the viewer sorts and filters in the
// browser: the page reads every order, so nothing is hidden on another page.
export function OrdersPage({ user }: Props) {
  const [orders, setOrders] = useState<Order[]>();
  const [suppliers, setSuppliers] = useState<Supplier[]>([]);
  const [form, setForm] = useState(emptyForm);
  const [placed, setPlaced] = useState<Order>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  // Order statuses move as others work, and nobody notifies purchasing of
  // that, so the list is re-read every few seconds.
  const tick = useTicker(4000);
  const purchasing = user.groups.includes("purchasing");

  useEffect(() => {
    api.orders().then(setOrders, (e: Error) => setError(e.message));
  }, [tick]);

  useEffect(() => {
    api.suppliers().then(setSuppliers, () => setSuppliers([]));
  }, []);

  const amount = Number(form.amount);
  const valid = form.supplier.trim() !== "" && form.description.trim() !== "" && Number.isInteger(amount) && amount > 0;
  const registered = registryEntry(suppliers, form.supplier);

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
          Every purchase, from its approval and purchase order to its invoice.
        </Typography>
      </Box>

      {error && (
        <Alert severity="error" onClose={() => setError(undefined)}>
          {error}
        </Alert>
      )}

      <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", lg: "360px minmax(0, 1fr)" }, alignItems: "start" }}>
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
              <Autocomplete
                freeSolo
                options={suppliers.map((s) => s.name)}
                inputValue={form.supplier}
                onInputChange={(_event, supplier) => setForm((current) => ({ ...current, supplier }))}
                renderInput={(params) => (
                  <TextField
                    {...params}
                    label="Supplier"
                    required
                    helperText={
                      registered
                        ? `On the supplier registry: its purchase order is sent to ${registered.contact}.`
                        : form.supplier.trim()
                          ? "Not on the supplier registry: you will upload a purchase order agreed with them."
                          : "Choose a registered supplier, or type another."
                    }
                  />
                )}
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
                A budget holder approves the order before anything is ordered from the supplier.
              </Typography>
            </Stack>
          ) : (
            <Alert severity="info">
              Only purchasing places orders. Sign in as erin to try it; as {user.name} you take part in their later steps.
            </Alert>
          )}

          {placed && (
            <Alert severity="success" sx={{ mt: 2 }} onClose={() => setPlaced(undefined)}>
              <AppLink href={orderPath(placed.id)}>{placed.id}</AppLink> placed. It waits for a budget holder&apos;s
              approval.
            </Alert>
          )}
        </Paper>

        <Paper sx={{ overflow: "hidden" }}>
          <DataGrid
            aria-label="Orders"
            rows={orders ?? []}
            columns={columns}
            loading={!orders}
            showToolbar
            initialState={{ pagination: { paginationModel: { pageSize: 10 } } }}
            pageSizeOptions={[10, 25, 50]}
            onRowClick={({ row }) => navigate(orderPath(row.id))}
            localeText={{ noRowsLabel: "No orders yet." }}
            sx={{ "& .MuiDataGrid-row": { cursor: "pointer" } }}
          />
        </Paper>
      </Box>
    </Stack>
  );
}
