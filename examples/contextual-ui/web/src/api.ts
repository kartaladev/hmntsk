// The shapes the page reads from the hmntsk HTTP contracts and from the demo
// application's own API, and a small client for both. The session cookie is
// HttpOnly and travels with every same-origin request, so no call names the
// user: the server decides who is asking.

type Correlation = {
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

type Page = { tasks: Task[]; nextCursor?: string };

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

export type DemoUser = { id: string; name: string; role: string; groups: string[] };

// Supplier is an entry on the supplier registry: the application sends its
// purchase orders to the contact itself.
export type Supplier = { name: string; contact: string };

export type OrderStatus =
  | "pending-approval"
  | "declined"
  | "awaiting-purchase-order"
  | "awaiting-invoice"
  | "invoice-review"
  | "invoice-approval"
  | "disputed"
  | "approved"
  | "rejected";

export type Order = {
  id: string;
  supplier: string;
  supplierRegistered: boolean;
  description: string;
  amount: number;
  requestedBy: string;
  status: OrderStatus;
  // invoiceId is absent until the supplier's invoice arrives.
  invoiceId?: string;
  // invoiceExpectedAt is when it arrives, once the purchase order has gone.
  invoiceExpectedAt?: string;
  createdAt: string;
};

export type NewOrder = Pick<Order, "supplier" | "description" | "amount">;

export type Invoice = { id: string; orderId: string; supplier: string; amount: number; receivedAt: string };

// OrderDocument is a purchase order, sent to a registered supplier or uploaded.
export type OrderDocument = {
  id: string;
  orderId: string;
  fileName: string;
  contentType: string;
  size: number;
  method: "sent" | "uploaded";
  sentTo?: string;
  createdBy: string;
  createdAt: string;
};

// RecordTask is one task on an order, as the application's record page reads
// it through the engine.
export type RecordTask = {
  id: string;
  type: string;
  status: string;
  // terminal is hmntsk's verdict that nothing can move the task on.
  terminal: boolean;
  assignee?: string;
  activityKey: string;
  priority: number;
  dueAt?: string;
  createdAt: string;
};

export type OrderRecord = {
  order: Order;
  invoice: Invoice | null;
  documents: OrderDocument[];
  tasks: RecordTask[];
};

// Issued is what issuing a purchase order answers: its document, and the task
// the application completed with it.
export type Issued = { document: OrderDocument; task: Task };

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

// isStale reports that the task changed since the page read it. The contract
// answers 409 both for a version the page no longer holds and for an operation
// the task's current state refuses, such as claiming a task someone else just
// claimed; either way the page's copy is out of date and should be read again.
export function isStale(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409;
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  // A form, such as an upload, sets its own multipart content type.
  const form = body instanceof FormData;

  const response = await fetch(path, {
    method,
    headers: body === undefined || form ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : form ? body : JSON.stringify(body),
  });

  if (!response.ok) {
    let code = "http_" + response.status;
    let message = response.statusText;
    let issues: Issue[] = [];

    // The task contract answers a JSON error; the demo API answers plain text.
    const text = await response.text();

    try {
      const failure = JSON.parse(text) as { error?: { code?: string; message?: string; issues?: Issue[] } };
      code = failure.error?.code ?? code;
      message = failure.error?.message ?? message;
      issues = failure.error?.issues ?? [];
    } catch {
      message = text.trim() || message;
    }

    throw new ApiError(response.status, code, message, issues);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

const orderApi = (id: string) => "/demo/orders/" + encodeURIComponent(id);

// documentPath downloads a purchase order document.
export function documentPath(id: string): string {
  return "/demo/documents/" + encodeURIComponent(id);
}

export const api = {
  users: () => call<DemoUser[]>("GET", "/demo/users"),
  session: () => call<DemoUser>("GET", "/demo/session"),
  signIn: (id: string) => call<DemoUser>("POST", "/demo/session", { user: id }),
  signOut: () => call<void>("DELETE", "/demo/session"),

  suppliers: () => call<{ suppliers: Supplier[] }>("GET", "/demo/suppliers").then((r) => r.suppliers),
  orders: () => call<{ orders: Order[] | null }>("GET", "/demo/orders").then((r) => r.orders ?? []),
  placeOrder: (order: NewOrder) => call<Order>("POST", "/demo/orders", order),
  order: (id: string) => call<OrderRecord>("GET", orderApi(id)),
  sendPurchaseOrder: (orderId: string, task: Pick<Task, "id" | "version">) =>
    call<Issued>("POST", orderApi(orderId) + "/purchase-order/send", { taskId: task.id, version: task.version }),
  uploadPurchaseOrder: (orderId: string, task: Pick<Task, "id" | "version">, file: File) => {
    const form = new FormData();
    form.set("taskId", task.id);
    form.set("version", String(task.version));
    form.set("file", file);

    return call<Issued>("POST", orderApi(orderId) + "/purchase-order/upload", form);
  },

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
