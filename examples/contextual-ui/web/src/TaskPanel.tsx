import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import CircularProgress from "@mui/material/CircularProgress";
import Divider from "@mui/material/Divider";
import FormControlLabel from "@mui/material/FormControlLabel";
import MenuItem from "@mui/material/MenuItem";
import Stack from "@mui/material/Stack";
import Switch from "@mui/material/Switch";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";
import { useEffect, useMemo, useState } from "react";

import { ApiError, api, isStale, type Issue, type Task, type TaskType } from "./api";
import { Due } from "./format";
import { fieldsFromSchema, outputFromValues, withBooleanDefaults, type Field } from "./schemaForm";
import { StatusChip } from "./StatusChip";

type Props = {
  id: string;
  user: string;
  types: Record<string, TaskType>;
  onChange: () => void;
};

// TaskPanel is where the work is done, inside the invoice page: the lifecycle
// actions the current user may take, and a form rendered from the task type's
// output schema.
export function TaskPanel({ id, user, types, onChange }: Props) {
  const [task, setTask] = useState<Task>();
  const [problem, setProblem] = useState<string>();
  const [issues, setIssues] = useState<Issue[]>([]);
  const [values, setValues] = useState<Record<string, string | boolean>>({});
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.task(id).then(
      (t) => {
        setTask(t);
        setValues(initialValues(t.progress));
      },
      // Reading one task is participants-only by default: a 403 here is the
      // server's policy, not a broken page.
      (e: ApiError) =>
        setProblem(
          e.status === 403
            ? "You take no part in this task, so you may not open it. The invoice's workflow is still shown."
            : e.message,
        ),
    );
  }, [id]);

  const type = task ? types[task.type] : undefined;
  const fields = useMemo(() => fieldsFromSchema(type?.outputSchema), [type]);

  const header = (
    <Box sx={{ px: 3, py: 2 }}>
      <Typography variant="overline" color="text.secondary">
        Your task
      </Typography>
      <Typography variant="h6" component="h2">
        {type?.title ?? task?.type ?? "Task"}
      </Typography>
    </Box>
  );

  if (problem && !task) {
    return (
      <Box>
        {header}
        <Alert severity="info" sx={{ mx: 3, mb: 3 }}>
          {problem}
        </Alert>
      </Box>
    );
  }

  if (!task) {
    return (
      <Box>
        {header}
        <Box sx={{ display: "flex", justifyContent: "center", p: 6 }}>
          <CircularProgress />
        </Box>
      </Box>
    );
  }

  const mine = task.assignee === user;

  const act = async (operation: string, body?: Record<string, unknown>) => {
    setIssues([]);
    setProblem(undefined);
    setBusy(true);

    try {
      const updated = await api.operate(task.id, operation, { version: task.version, ...body });
      setTask(updated);
      onChange();
    } catch (e) {
      if (isStale(e)) {
        // Someone else acted on the task, or it moved on its own: show it as it
        // is now, keeping whatever the form holds.
        try {
          setTask(await api.task(task.id));
          setProblem("This task changed while you had it open. It now shows the latest; try again if you still can.");
        } catch (reread) {
          setProblem((reread as Error).message);
        }

        onChange();
      } else if (e instanceof ApiError) {
        setIssues(e.issues);
        if (e.issues.length === 0) {
          setProblem(`${e.code}: ${e.message}`);
        }
      }
    } finally {
      setBusy(false);
    }
  };

  const submit = (operation: "progress" | "complete") => {
    // Completing submits what the form shows, so an untouched switch counts as
    // false; saving progress records only what the user actually set.
    const submitted = operation === "complete" ? withBooleanDefaults(fields, values) : values;
    const { output, errors } = outputFromValues(fields, submitted);
    setFieldErrors(errors);

    if (Object.keys(errors).length > 0) {
      return;
    }

    if (operation === "complete") {
      return act("complete", { output });
    }

    // Progress is a JSON Patch: partial work is merged, never validated for
    // completeness, so a half-filled form survives a reload.
    const patch = Object.entries(output).map(([name, value]) => ({ op: "add", path: "/" + name, value }));

    return act("progress", { patch });
  };

  const unplaced = issues.filter((i) => !fields.some((f) => i.pointer === "/" + f.name));

  return (
    <Box>
      {header}
      <Divider />
      <Stack spacing={3} sx={{ p: 3 }}>
        {type?.description && <Typography color="text.secondary">{type.description}</Typography>}

        <Stack direction="row" spacing={1} sx={{ alignItems: "center", flexWrap: "wrap" }}>
          <StatusChip status={task.status} />
          <Typography variant="body2" color="text.secondary">
            {task.assignee ? `held by ${task.assignee}` : "in the pool"}
          </Typography>
          {task.dueAt && (
            <Stack direction="row" spacing={0.5} sx={{ alignItems: "center" }}>
              <Typography variant="body2" color="text.secondary">
                · due
              </Typography>
              <Due at={task.dueAt} />
            </Stack>
          )}
        </Stack>

        {problem && <Alert severity="error">{problem}</Alert>}

        {(task.status === "READY" || (task.status === "RESERVED" && mine)) && (
          <Stack direction="row" spacing={1}>
            {task.status === "READY" && (
              <Button variant="contained" disabled={busy} onClick={() => void act("claim")}>
                Claim
              </Button>
            )}
            {task.status === "RESERVED" && mine && (
              <>
                <Button variant="contained" disabled={busy} onClick={() => void act("start")}>
                  Start
                </Button>
                <Button variant="outlined" disabled={busy} onClick={() => void act("release")}>
                  Release
                </Button>
              </>
            )}
          </Stack>
        )}

        {task.status === "RESERVED" && !mine && (
          <Typography variant="body2" color="text.secondary">
            {task.assignee} has claimed this task.
          </Typography>
        )}

        {task.status === "IN_PROGRESS" && mine && (
          <Box
            component="form"
            noValidate
            onSubmit={(e) => {
              e.preventDefault();
              void submit("complete");
            }}
          >
            <Typography variant="subtitle1" sx={{ fontWeight: 700, mb: 2 }}>
              Decision
            </Typography>
            <Stack spacing={2}>
              {fields.map((field) => (
                <FieldInput
                  key={field.name}
                  field={field}
                  value={values[field.name]}
                  error={fieldErrors[field.name] ?? issues.find((i) => i.pointer === "/" + field.name)?.detail}
                  onChange={(v) => setValues((current) => ({ ...current, [field.name]: v }))}
                />
              ))}
              {unplaced.length > 0 && (
                <Alert severity="error">
                  {unplaced.map((i) => (
                    <div key={(i.pointer ?? "") + i.detail}>
                      {i.pointer ? `${i.pointer}: ` : ""}
                      {i.detail}
                    </div>
                  ))}
                </Alert>
              )}
              <Stack direction="row" spacing={1} sx={{ justifyContent: "flex-end" }}>
                <Button variant="outlined" disabled={busy} onClick={() => void submit("progress")}>
                  Save progress
                </Button>
                <Button type="submit" variant="contained" disabled={busy}>
                  Complete
                </Button>
              </Stack>
            </Stack>
          </Box>
        )}

        {task.output && (
          <Box>
            <Typography variant="subtitle1" sx={{ fontWeight: 700, mb: 1 }}>
              Decision recorded
            </Typography>
            <Box
              component="pre"
              sx={{ m: 0, p: 2, borderRadius: 2, bgcolor: "action.hover", overflowX: "auto", fontSize: 13 }}
            >
              {JSON.stringify(task.output, null, 2)}
            </Box>
          </Box>
        )}
      </Stack>
    </Box>
  );
}

function FieldInput({
  field,
  value,
  error,
  onChange,
}: {
  field: Field;
  value: string | boolean | undefined;
  error?: string;
  onChange: (value: string | boolean) => void;
}) {
  if (field.kind === "boolean") {
    return (
      <Box>
        <FormControlLabel
          control={<Switch checked={value === true} onChange={(e) => onChange(e.target.checked)} />}
          label={`${field.name}${field.required ? " *" : ""}`}
        />
        {error && (
          <Typography variant="caption" color="error" component="p">
            {error}
          </Typography>
        )}
      </Box>
    );
  }

  return (
    <TextField
      label={field.name}
      required={field.required}
      value={String(value ?? "")}
      onChange={(e) => onChange(e.target.value)}
      error={Boolean(error)}
      helperText={error ?? (field.kind === "json" ? "JSON" : undefined)}
      fullWidth
      select={field.kind === "enum"}
      multiline={field.kind === "json"}
      minRows={field.kind === "json" ? 3 : undefined}
      type={field.kind === "number" || field.kind === "integer" ? "number" : "text"}
    >
      {field.kind === "enum" &&
        field.options?.map((option) => (
          <MenuItem key={option} value={option}>
            {option}
          </MenuItem>
        ))}
    </TextField>
  );
}

function initialValues(progress: Record<string, unknown> | undefined): Record<string, string | boolean> {
  const values: Record<string, string | boolean> = {};

  for (const [name, value] of Object.entries(progress ?? {})) {
    values[name] = typeof value === "boolean" ? value : typeof value === "string" ? value : JSON.stringify(value);
  }

  return values;
}
