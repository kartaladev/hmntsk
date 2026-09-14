// The application's pages and the paths between them. The page keeps its own
// small router rather than a library: four pages and history.pushState are all
// it needs, and every rule here is tested.

export type PageRoute =
  | { page: "login"; next?: string }
  | { page: "inbox" }
  | { page: "orders" }
  | { page: "invoice"; invoiceId: string; activity?: string; taskId?: string }
  | { page: "notFound" };

// matchPage names the page a location shows. An invoice page's path is the
// task types' hmntsk.route, /invoices/{ownerRef}/{activityKey}?task={id}, and
// works without the activity and task too.
export function matchPage(location: { pathname: string; search: string }): PageRoute {
  const segments = location.pathname.split("/").filter(Boolean).map(decodeURIComponent);
  const query = new URLSearchParams(location.search);

  switch (segments[0]) {
    case undefined:
      return { page: "inbox" };
    case "orders":
      return segments.length === 1 ? { page: "orders" } : { page: "notFound" };
    case "login": {
      const next = query.get("next");

      return segments.length === 1 ? { page: "login", ...(next ? { next } : {}) } : { page: "notFound" };
    }
    case "invoices": {
      const [, invoiceId, activity] = segments;
      if (!invoiceId || segments.length > 3) {
        return { page: "notFound" };
      }

      const taskId = query.get("task");

      return { page: "invoice", invoiceId, ...(activity ? { activity } : {}), ...(taskId ? { taskId } : {}) };
    }
    default:
      return { page: "notFound" };
  }
}

// afterSignIn is where a viewer goes once signed in: back to the page they
// opened, or else where their role starts. A return address is only ever a
// path on this origin, so the sign-in page cannot be used to send someone
// elsewhere.
export function afterSignIn(next: string | undefined, user: { groups: string[] }): string {
  if (next && isLocalPath(next) && matchPage(new URL(next, "http://local.invalid")).page !== "login") {
    return next;
  }

  return user.groups.includes("purchasing") ? "/orders" : "/";
}

// isLocalPath reports a path on this origin, the only kind of link the page
// follows itself.
export function isLocalPath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//") && !path.startsWith("/\\");
}

// signInPath is the sign-in page, remembering the page the viewer came from.
export function signInPath(from: string): string {
  return from === "/" ? "/login" : "/login?" + new URLSearchParams({ next: from }).toString();
}

export function invoicePath(invoiceId: string): string {
  return "/invoices/" + encodeURIComponent(invoiceId);
}

// navigate moves to a page without reloading. Every page reads the location
// through usePage, which hears this and the browser's back and forward.
export function navigate(path: string, options: { replace?: boolean } = {}) {
  if (options.replace) {
    window.history.replaceState(null, "", path);
  } else {
    window.history.pushState(null, "", path);
  }

  window.dispatchEvent(new PopStateEvent("popstate"));
}

// isPlainClick reports a click the page should handle itself; a modified or
// middle click keeps the browser's own behaviour, such as opening a new tab.
export function isPlainClick(event: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean }) {
  return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
}
