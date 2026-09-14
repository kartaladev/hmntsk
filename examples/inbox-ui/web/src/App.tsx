import AddCircleOutlineIcon from "@mui/icons-material/AddCircleOutlined";
import DarkModeIcon from "@mui/icons-material/DarkMode";
import InboxIcon from "@mui/icons-material/Inbox";
import LightModeIcon from "@mui/icons-material/LightMode";
import Alert from "@mui/material/Alert";
import AppBar from "@mui/material/AppBar";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Container from "@mui/material/Container";
import Drawer from "@mui/material/Drawer";
import IconButton from "@mui/material/IconButton";
import Link from "@mui/material/Link";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import MenuItem from "@mui/material/MenuItem";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import { useColorScheme } from "@mui/material/styles";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import TextField from "@mui/material/TextField";
import Toolbar from "@mui/material/Toolbar";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { useCallback, useEffect, useMemo, useState } from "react";

import { api, currentUser, type DemoUser, type Task, type TaskType } from "./api";
import { bucketQuery, buckets as bucketsFor, taskListQuery } from "./buckets";
import { contextLink, taskFromLocation } from "./links";
import { NotificationBell } from "./Notifications";
import { StatusChip } from "./StatusChip";
import { TaskPanel } from "./TaskPanel";

export function App() {
  const [users, setUsers] = useState<DemoUser[]>([]);
  const [user, setUser] = useState(currentUser());
  const [types, setTypes] = useState<Record<string, TaskType>>({});
  const [bucketName, setBucketName] = useState("available");
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [tasks, setTasks] = useState<Task[]>([]);
  const [nextCursor, setNextCursor] = useState<string>();
  const [selected, setSelected] = useState(taskFromLocation(window.location));
  const [error, setError] = useState<string>();
  const [revision, setRevision] = useState(0);

  // A refresh rebuilds the buckets, so "overdue" is measured from now, and that
  // is what re-reads the counts and the list below.
  const buckets = useMemo(() => bucketsFor(new Date()), [revision]);
  const bucket = buckets.find((b) => b.name === bucketName) ?? buckets[0]!;

  const refresh = useCallback(() => setRevision((r) => r + 1), []);

  useEffect(() => {
    api.users().then(setUsers, (e: Error) => setError(e.message));
    api.taskTypes().then(
      ({ types }) => setTypes(Object.fromEntries(types.map((t) => [t.name, t]))),
      (e: Error) => setError(e.message),
    );
  }, []);

  useEffect(() => {
    const onPop = () => setSelected(taskFromLocation(window.location));
    window.addEventListener("popstate", onPop);

    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // Counts change with the user and on a refresh, not when another bucket is
  // chosen.
  useEffect(() => {
    if (!user) {
      setCounts({});

      return;
    }

    let cancelled = false;

    Promise.all(buckets.map((b) => api.count(bucketQuery(b)).then(({ count }) => [b.name, count] as const)))
      .then((entries) => !cancelled && setCounts(Object.fromEntries(entries)))
      .catch((e: Error) => !cancelled && setError(e.message));

    return () => {
      cancelled = true;
    };
  }, [user, buckets]);

  useEffect(() => {
    if (!user) {
      setTasks([]);

      return;
    }

    let cancelled = false;

    api
      .tasks(taskListQuery(bucket))
      .then((page) => {
        if (!cancelled) {
          setTasks(page.tasks);
          setNextCursor(page.nextCursor);
          setError(undefined);
        }
      })
      .catch((e: Error) => !cancelled && setError(e.message));

    return () => {
      cancelled = true;
    };
  }, [user, bucket]);

  const chooseUser = async (id: string) => {
    await api.chooseUser(id);
    setUser(id);
    refresh();
  };

  const loadMore = async () => {
    if (!nextCursor) {
      return;
    }

    const page = await api.tasks(taskListQuery(bucket, nextCursor));
    setTasks((current) => [...current, ...page.tasks]);
    setNextCursor(page.nextCursor);
  };

  const open = (task: Task) => {
    const link = contextLink(task, types[task.type]) ?? `/tasks?task=${encodeURIComponent(task.id)}`;
    window.history.pushState(null, "", link);
    setSelected(task.id);
  };

  const close = () => {
    window.history.pushState(null, "", "/");
    setSelected(undefined);
  };

  return (
    <Box sx={{ minHeight: "100vh", bgcolor: "background.default" }}>
      <AppBar position="sticky" color="inherit" elevation={0} sx={{ borderBottom: 1, borderColor: "divider" }}>
        <Toolbar sx={{ gap: 2, flexWrap: "wrap", py: 1 }}>
          <InboxIcon color="primary" />
          <Typography variant="h6" component="h1" sx={{ flexGrow: 1 }}>
            Invoice inbox
          </Typography>
          <TextField
            select
            size="small"
            label="Acting as"
            value={user}
            onChange={(e) => void chooseUser(e.target.value)}
            sx={{ minWidth: 240 }}
          >
            {users.map((u) => (
              <MenuItem key={u.id} value={u.id}>
                {u.id}{" "}
                <Typography component="span" variant="body2" sx={{ ml: 1, color: "text.secondary" }}>
                  {u.groups.join(", ")}
                </Typography>
              </MenuItem>
            ))}
          </TextField>
          {/* No refresh here: the new task's notification signal refreshes whoever it concerns. */}
          <Button
            variant="contained"
            startIcon={<AddCircleOutlineIcon />}
            onClick={() => void api.simulateInvoice().catch((e: Error) => setError(e.message))}
          >
            Simulate a new invoice
          </Button>
          {user && <NotificationBell user={user} onChange={refresh} />}
          <ColorModeToggle />
        </Toolbar>
      </AppBar>

      <Container maxWidth="xl" sx={{ py: 3 }}>
        <Alert severity="warning" variant="outlined" sx={{ mb: 3 }}>
          <strong>Demo only.</strong> The user switcher sets a cookie the server trusts; that is not authentication.
          This page illustrates hmntsk&apos;s HTTP contracts and is not a UI library.
        </Alert>

        {error && (
          <Alert severity="error" onClose={() => setError(undefined)} sx={{ mb: 3 }}>
            {error}
          </Alert>
        )}

        {!user ? (
          <Paper sx={{ p: 6, textAlign: "center" }}>
            <InboxIcon color="disabled" sx={{ fontSize: 56 }} />
            <Typography variant="h6" sx={{ mt: 1 }}>
              Choose a demo user to see their inbox
            </Typography>
            <Typography color="text.secondary">
              alice and bob approve invoices, carol manages them, dave audits.
            </Typography>
          </Paper>
        ) : (
          <Box sx={{ display: "grid", gap: 3, gridTemplateColumns: { xs: "1fr", md: "240px 1fr" } }}>
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
              <Stack direction="row" sx={{ px: 2, py: 1.5, alignItems: "center" }}>
                <Typography variant="subtitle1" sx={{ fontWeight: 700, flexGrow: 1 }}>
                  {bucket.label}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  most urgent first
                </Typography>
              </Stack>
              <TableContainer>
                <Table size="small" aria-label={bucket.label}>
                  <TableHead>
                    <TableRow>
                      <TableCell>Task</TableCell>
                      <TableCell>Invoice</TableCell>
                      <TableCell align="center">Priority</TableCell>
                      <TableCell>Due</TableCell>
                      <TableCell>Status</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {tasks.map((task) => (
                      <TableRow key={task.id} hover selected={task.id === selected} onClick={() => open(task)} sx={{ cursor: "pointer" }}>
                        <TableCell>
                          <Link
                            href={contextLink(task, types[task.type])}
                            underline="hover"
                            onClick={(e) => {
                              e.preventDefault();
                              e.stopPropagation();
                              open(task);
                            }}
                          >
                            {types[task.type]?.title ?? task.type}
                          </Link>
                        </TableCell>
                        <TableCell>{task.correlation?.ownerRef}</TableCell>
                        <TableCell align="center">
                          <Chip size="small" variant="outlined" label={`P${task.priority}`} color={task.priority <= 1 ? "error" : "default"} />
                        </TableCell>
                        <TableCell>
                          <Due at={task.dueAt} />
                        </TableCell>
                        <TableCell>
                          <StatusChip status={task.status} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </TableContainer>
              {tasks.length === 0 && (
                <Typography color="text.secondary" sx={{ p: 4, textAlign: "center" }}>
                  Nothing in this bucket.
                </Typography>
              )}
              {nextCursor && (
                <Box sx={{ p: 2, textAlign: "center" }}>
                  <Button onClick={() => void loadMore()}>Load more</Button>
                </Box>
              )}
            </Paper>
          </Box>
        )}
      </Container>

      <Drawer
        anchor="right"
        open={Boolean(user && selected)}
        onClose={close}
        slotProps={{ paper: { sx: { width: { xs: "100%", sm: 460 } } } }}
      >
        {/* Keyed by task only: the panel keeps the task its own actions returned, and unsaved form values. */}
        {user && selected && (
          <TaskPanel key={selected} id={selected} user={user} types={types} onChange={refresh} onClose={close} />
        )}
      </Drawer>
    </Box>
  );
}

function Due({ at }: { at?: string }) {
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

function ColorModeToggle() {
  const { mode, systemMode, setMode } = useColorScheme();

  // mode is undefined on the first render, before the stored preference is read.
  if (!mode) {
    return null;
  }

  const dark = (mode === "system" ? systemMode : mode) === "dark";

  return (
    <Tooltip title={dark ? "Light mode" : "Dark mode"}>
      <IconButton onClick={() => setMode(dark ? "light" : "dark")} aria-label="Toggle colour mode">
        {dark ? <LightModeIcon /> : <DarkModeIcon />}
      </IconButton>
    </Tooltip>
  );
}
