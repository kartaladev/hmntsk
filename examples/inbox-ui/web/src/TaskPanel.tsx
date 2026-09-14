import CloseIcon from "@mui/icons-material/Close";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import CircularProgress from "@mui/material/CircularProgress";
import Divider from "@mui/material/Divider";
import FormControlLabel from "@mui/material/FormControlLabel";
import IconButton from "@mui/material/IconButton";
import MenuItem from "@mui/material/MenuItem";
import Stack from "@mui/material/Stack";
import Switch from "@mui/material/Switch";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";
import { useEffect, useMemo, useState } from "react";

import { ApiError, api, type Issue, type Task, type TaskType } from "./api";
import { fieldsFromSchema, outputFromValues, withBooleanDefaults, type Field } from "./schemaForm";
import { StatusChip } from "./StatusChip";

type Props = {
  id: string;
  user: string;
  types: Record<string, TaskType>;
  onChange: () => void;
  onClose: () => void;
};

// TaskPanel is where the work is done: the invoice the task is about, the
// lifecycle actions the current user may take, and a form rendered from the
// task type's output schema.
export function TaskPanel({ id, user, types, onChange, onClose }: Props) {
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
      (e: ApiError) => setProblem(e.status === 403 ? "You take no part in this task, so you may not read it." : e.message),
    );
  }, [id]);

  const type = task ? types[task.type] : undefined;
  const fields = useMemo(() => fieldsFromSchema(type?.outputSchema), [type]);

  const header = (
    <Stack direction="row" sx={{ alignItems: "center", px: 3, py: 2 }}>
      <Typography variant="h6" component="h2" sx={{ flexGrow: 1 }}>
        {type?.title ?? task?.type ?? "Task"}
      </Typography>
      <IconButton onClick={onClose} aria-label="Close">
        <CloseIcon />
      </IconButton>
    </Stack>
  );

  if (problem && !task) {
    return (
      <Box>
        {header}
        <Alert severity="error" sx={{ mx: 3 }}>
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
      if (e instanceof ApiError) {
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

        <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
          <StatusChip status={task.status} />
          {task.assignee && (
            <Typography variant="body2" color="text.secondary">
              held by {task.assignee}
            </Typography>
          )}
        </Stack>

        <Box
          component="dl"
          sx={{ display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 2, rowGap: 1, m: 0 }}
        >
          <Detail label="Invoice" value={task.correlation?.ownerRef} />
          {Object.entries(task.input ?? {}).map(([k, v]) => (
            <Detail key={k} label={k} value={String(v)} />
          ))}
        </Box>

        {problem && <Alert severity="error">{problem}</Alert>}

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
              Output
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

function Detail({ label, value }: { label: string; value?: string }) {
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
