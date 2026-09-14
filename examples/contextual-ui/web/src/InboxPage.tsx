import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import Typography from "@mui/material/Typography";
import { useEffect, useMemo, useState } from "react";

import { api, type DemoUser, type Task, type TaskType } from "./api";
import { AppLink } from "./AppLink";
import { bucketQuery, buckets as bucketsFor, taskListQuery } from "./buckets";
import { Due } from "./format";
import { contextLink } from "./links";
import { invoicePath, navigate } from "./pages";
import { StatusChip } from "./StatusChip";

type Props = {
  user: DemoUser;
  types: Record<string, TaskType>;
  // revision changes when something the inbox shows may have changed.
  revision: number;
};

// InboxPage is the signed-in user's work across the application: buckets with
// counts, and each task linked to the invoice page where it is done.
export function InboxPage({ user, types, revision }: Props) {
  const [bucketName, setBucketName] = useState("available");
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [tasks, setTasks] = useState<Task[]>([]);
  const [nextCursor, setNextCursor] = useState<string>();
  const [error, setError] = useState<string>();

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

  useEffect(() => {
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
  }, [user.id, bucket]);

  const loadMore = async () => {
    if (!nextCursor) {
      return;
    }

    const page = await api.tasks(taskListQuery(bucket, nextCursor));
    setTasks((current) => [...current, ...page.tasks]);
    setNextCursor(page.nextCursor);
  };

  // A task opens where its work is done: the type's hmntsk.route, which is the
  // invoice page.
  const linkOf = (task: Task) =>
    contextLink(task, types[task.type]) ?? invoicePath(task.correlation?.ownerRef ?? "");

  return (
    <Stack spacing={3}>
      <Box>
        <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
          Inbox
        </Typography>
        <Typography color="text.secondary">Invoice work waiting for {user.name}, most urgent first.</Typography>
      </Box>

      {error && (
        <Alert severity="error" onClose={() => setError(undefined)}>
          {error}
        </Alert>
      )}

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
          <Typography variant="subtitle1" component="h2" sx={{ fontWeight: 700, px: 2, py: 1.5 }}>
            {bucket.label}
          </Typography>
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
                  <TableRow key={task.id} hover onClick={() => navigate(linkOf(task))} sx={{ cursor: "pointer" }}>
                    <TableCell>
                      <AppLink href={linkOf(task)} underline="hover" onClick={(e) => e.stopPropagation()}>
                        {types[task.type]?.title ?? task.type}
                      </AppLink>
                    </TableCell>
                    <TableCell>{task.correlation?.ownerRef}</TableCell>
                    <TableCell align="center">
                      <Chip
                        size="small"
                        variant="outlined"
                        label={`P${task.priority}`}
                        color={task.priority <= 1 ? "error" : "default"}
                      />
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
    </Stack>
  );
}
