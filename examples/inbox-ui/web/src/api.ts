// The shapes the page reads from the hmntsk HTTP contracts, and a small client
// for them. The demo cookie travels with every same-origin request, so no call
// names the user: the server decides who is asking.

export type Correlation = {
  ownerType?: string;
  ownerRef?: string;
  activityKey?: string;
  extra?: Record<string, string>;
};

export type Task = {
  id: string;
  type: string;
  version: number;
  status: string;
  priority: number;
  assignee?: string;
  correlation?: Correlation;
  input?: Record<string, unknown>;
  progress?: Record<string, unknown>;
  output?: Record<string, unknown>;
  createdAt: string;
  dueAt?: string;
};

export type Page = { tasks: Task[]; nextCursor?: string };

export type TaskType = {
  name: string;
  title?: string;
  description?: string;
  inputSchema?: unknown;
  outputSchema?: unknown;
  metadata?: Record<string, string>;
};

export type Notification = {
  id: string;
  kind: string;
  state: string;
  title?: string;
  links?: Record<string, string>;
  createdAt: string;
};

export type DemoUser = { id: string; groups: string[] };

export type Issue = { pointer?: string; detail: string };

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly issues: Issue[] = [],
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!response.ok) {
    let code = "http_" + response.status;
    let message = response.statusText;
    let issues: Issue[] = [];

    try {
      const failure = (await response.json()) as { error?: { code?: string; message?: string; issues?: Issue[] } };
      code = failure.error?.code ?? code;
      message = failure.error?.message ?? message;
      issues = failure.error?.issues ?? [];
    } catch {
      // Not a contract error body; the status says enough.
    }

    throw new ApiError(response.status, code, message, issues);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

export const api = {
  users: () => call<DemoUser[]>("GET", "/demo/users"),
  chooseUser: (id: string) => call<void>("POST", "/demo/user?name=" + encodeURIComponent(id)),
  simulateInvoice: () => call<{ invoice: string; task: string }>("POST", "/demo/invoices"),

  taskTypes: () => call<{ types: TaskType[] }>("GET", "/v1/task-types"),
  tasks: (query: string) => call<Page>("GET", "/v1/tasks?" + query),
  count: (query: string) => call<{ count: number }>("GET", "/v1/tasks/count?" + query),
  task: (id: string) => call<Task>("GET", "/v1/tasks/" + encodeURIComponent(id)),
  operate: (id: string, operation: string, body: Record<string, unknown> = {}) =>
    call<Task>("POST", `/v1/tasks/${encodeURIComponent(id)}/${operation}`, body),

  notifications: () => call<{ notifications: Notification[] }>("GET", "/v1/notifications?state=ACTIVE&limit=10"),
  unread: () => call<{ count: number }>("GET", "/v1/notifications/count"),
  markRead: (id: string) => call<Notification>("POST", `/v1/notifications/${encodeURIComponent(id)}/read`),
};

// currentUser reads the demo cookie. Only a demo would read identity this way.
export function currentUser(): string {
  const match = document.cookie.split("; ").find((pair) => pair.startsWith("demo_user="));

  return match ? decodeURIComponent(match.slice("demo_user=".length)) : "";
}
