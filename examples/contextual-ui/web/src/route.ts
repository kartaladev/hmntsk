import type { Task } from "./api";

// A task as the HTTP contract serves it, reduced to what a route needs.
export type RouteTask = Pick<Task, "id" | "type" | "correlation">;

const extraPrefix = "correlation.extra.";

// expandRoute is hmntsk.ExpandRoute in the browser: it fills a type's
// hmntsk.route template for one task, so the page can link the task to where
// its work is done without asking the server.
//
// Values are inserted raw, exactly as the Go helper does. Escape for wherever
// the link points before following it.
export function expandRoute(template: string, task: RouteTask): string {
  // One pass over innermost {…} pairs: a value that itself contains braces is
  // never expanded again, and anything unrecognised is left for a later pass.
  return template.replace(/\{([^{}]*)\}/g, (placeholder, name: string) => {
    const value = lookup(name, task);

    return value === undefined ? placeholder : value;
  });
}

function lookup(name: string, task: RouteTask): string | undefined {
  const correlation = task.correlation ?? {};

  switch (name) {
    case "task.id":
      return task.id;
    case "task.type":
      return task.type;
    case "correlation.ownerType":
      return correlation.ownerType ?? "";
    case "correlation.ownerRef":
      return correlation.ownerRef ?? "";
    case "correlation.activityKey":
      return correlation.activityKey ?? "";
  }

  if (name.startsWith(extraPrefix)) {
    const key = name.slice(extraPrefix.length);

    // A key the task does not carry is not a known field: it stays as written.
    return correlation.extra && Object.hasOwn(correlation.extra, key) ? correlation.extra[key] : undefined;
  }

  return undefined;
}
