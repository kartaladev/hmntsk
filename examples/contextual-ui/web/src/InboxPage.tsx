import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";
import { DataGrid, type GridColDef, type GridPaginationModel } from "@mui/x-data-grid";
import { useEffect, useMemo, useRef, useState } from "react";

import { api, type DemoUser, type Task, type TaskType } from "./api";
import { AppLink } from "./AppLink";
import { bucketQuery, buckets as bucketsFor, pageSize, taskListQuery } from "./buckets";
import { Due } from "./format";
import { contextLink } from "./links";
import { navigate, orderPath } from "./pages";
import { cursorFor, rememberNext } from "./paging";
import { StatusChip } from "./StatusChip";

type Props = {
  user: DemoUser;
  types: Record<string, TaskType>;
  // revision changes when something the inbox shows may have changed.
  revision: number;
};

// InboxPage is the signed-in user's work across the application: buckets with
// counts, and each bucket's tasks in a data grid, linked to the order page where
// they are done.
export function InboxPage({ user, types, revision }: Props) {
  const [bucketName, setBucketName] = useState("available");
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [tasks, setTasks] = useState<Task[]>([]);
  const [paging, setPaging] = useState<GridPaginationModel>({ page: 0, pageSize });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();

  // cursors holds the cursor that starts each page reached so far: the grid
  // pages by number, and the task API by cursor.
  const cursors = useRef<(string | undefined)[]>([]);

  // A refresh rebuilds the buckets, so "overdue" is measured from now, and that
  // is what re-reads the counts and the list below.
  const buckets = useMemo(() => bucketsFor(new Date()), [revision]);
  const bucket = buckets.find((b) => b.name === bucketName) ?? buckets[0]!;

  // Counts change with the user and on a refresh, not when another bucket is
  // chosen.
  useEffect(() => {
    let cancelled = false;

    Promise.all(buckets.map((b) => api.count(bucketQuery(b)).then(({ count }) => [b.name, count] as const)))
      .then((entries) => !cancelled && setCounts(Object.fromEntries(entries)))
      .catch((e: Error) => !cancelled && setError(e.message));

    return () => {
      cancelled = true;
    };
  }, [user.id, buckets]);

  // Another bucket, or a refresh, starts again at the first page: the cursors
  // were computed from a list that has since changed.
  useEffect(() => {
    cursors.current = [];
    setPaging((current) => (current.page === 0 ? current : { ...current, page: 0 }));
  }, [user.id, bucket]);

  useEffect(() => {
    const cursor = cursorFor(cursors.current, paging.page);

    // A page is only asked for once the page before it said where it starts.
    if (cursor === null) {
      return;
    }

    let cancelled = false;
    setLoading(true);

    api
      .tasks(taskListQuery(bucket, cursor))
      .then((page) => {
        if (!cancelled) {
          cursors.current = rememberNext(cursors.current, paging.page, page.nextCursor);
          setTasks(page.tasks);
          setError(undefined);
        }
      })
      .catch((e: Error) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));

    return () => {
      cancelled = true;
    };
  }, [user.id, bucket, paging.page]);

  // A task opens where its work is done: the type's hmntsk.route, which is the
  // order page.
  const linkOf = (task: Task) => contextLink(task, types[task.type]) ?? orderPath(task.correlation?.ownerRef ?? "");

  const columns: GridColDef<Task>[] = [
    {
      field: "type",
      headerName: "Task",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => (
        <AppLink href={linkOf(row)} underline="hover" onClick={(e) => e.stopPropagation()}>
          {types[row.type]?.title ?? row.type}
        </AppLink>
      ),
    },
    { field: "order", headerName: "Order", width: 110, valueGetter: (_value, row) => row.correlation?.ownerRef },
    {
      field: "priority",
      headerName: "Priority",
      width: 90,
      align: "center",
      headerAlign: "center",
      renderCell: ({ row }) => (
        <Chip size="small" variant="outlined" label={`P${row.priority}`} color={row.priority <= 1 ? "error" : "default"} />
      ),
    },
    { field: "dueAt", headerName: "Due", width: 140, renderCell: ({ row }) => <Due at={row.dueAt} /> },
    { field: "status", headerName: "Status", width: 130, renderCell: ({ row }) => <StatusChip status={row.status} /> },
  ];

  return (
    <Stack spacing={3}>
      <Box>
        <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
          Inbox
        </Typography>
        <Typography color="text.secondary">Purchasing work waiting for {user.name}, most urgent first.</Typography>
      </Box>

      {error && (
        <Alert severity="error" onClose={() => setError(undefined)}>
          {error}
        </Alert>
      )}

      <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", md: "240px minmax(0, 1fr)" } }}>
        <Paper component="nav" aria-label="Buckets" sx={{ p: 1, alignSelf: "start" }}>
          <List disablePadding>
            {buckets.map((b) => (
              <ListItemButton
                key={b.name}
                selected={b.name === bucket.name}
                onClick={() => setBucketName(b.name)}
                sx={{ borderRadius: 2 }}
              >
                <ListItemText primary={b.label} />
                <Chip
                  size="small"
                  label={counts[b.name] ?? "…"}
                  color={b.name === "overdue" && (counts[b.name] ?? 0) > 0 ? "error" : "default"}
                />
              </ListItemButton>
            ))}
          </List>
        </Paper>

        <Paper sx={{ overflow: "hidden" }}>
          <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, px: 2, py: 1.5 }}>
            {bucket.label}
          </Typography>
          <DataGrid
            aria-label={bucket.label}
            rows={tasks}
            columns={columns}
            loading={loading}
            // The task API pages by cursor and knows each bucket's count, so
            // the grid shows one server page at a time.
            paginationMode="server"
            rowCount={counts[bucket.name] ?? -1}
            paginationMeta={{ hasNextPage: cursorFor(cursors.current, paging.page + 1) !== null }}
            paginationModel={paging}
            onPaginationModelChange={setPaging}
            pageSizeOptions={[pageSize]}
            // The server orders every page by urgency. Sorting or filtering a
            // single page in the browser would reorder that page alone, which
            // misleads, so the grid offers neither.
            disableColumnSorting
            disableColumnFilter
            disableColumnMenu
            onRowClick={({ row }) => navigate(linkOf(row))}
            localeText={{ noRowsLabel: "Nothing in this bucket." }}
            sx={{ "& .MuiDataGrid-row": { cursor: "pointer" } }}
          />
        </Paper>
      </Box>
    </Stack>
  );
}
