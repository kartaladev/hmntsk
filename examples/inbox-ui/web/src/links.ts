import type { Task, TaskType } from "./api";
import { expandRoute } from "./route";

// contextLink is where a task's work is done: the type's hmntsk.route, filled in
// for this task. This template puts correlation values in path segments and the
// task ID in the query, so each is escaped for that position first, because
// expandRoute, like hmntsk.ExpandRoute, inserts values raw.
export function contextLink(task: Task, type: TaskType | undefined): string | undefined {
  const template = type?.metadata?.["hmntsk.route"];
  if (!template) {
    return undefined;
  }

  const correlation = task.correlation ?? {};

  return expandRoute(template, {
    id: encodeURIComponent(task.id),
    type: encodeURIComponent(task.type),
    correlation: {
      ownerType: encodeURIComponent(correlation.ownerType ?? ""),
      ownerRef: encodeURIComponent(correlation.ownerRef ?? ""),
      activityKey: encodeURIComponent(correlation.activityKey ?? ""),
      extra: Object.fromEntries(
        Object.entries(correlation.extra ?? {}).map(([key, value]) => [key, encodeURIComponent(value)]),
      ),
    },
  });
}

// taskFromLocation reads the task a context link points at.
export function taskFromLocation(location: { search: string }): string | undefined {
  return new URLSearchParams(location.search).get("task") ?? undefined;
}
