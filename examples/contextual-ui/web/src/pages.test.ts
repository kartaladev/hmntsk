import { describe, expect, it } from "vitest";

import { afterSignIn, matchPage, orderPath, signInPath, type PageRoute } from "./pages";

describe("matchPage", () => {
  const cases: { name: string; path: string; expected: PageRoute }[] = [
    { name: "the root is the inbox", path: "/", expected: { page: "inbox" } },
    { name: "orders", path: "/orders", expected: { page: "orders" } },
    { name: "a trailing slash names the same page", path: "/orders/", expected: { page: "orders" } },
    { name: "sign-in with nowhere to return to", path: "/login", expected: { page: "login" } },
    {
      name: "sign-in remembers where to return",
      path: "/login?next=%2Forders%2FORD-101%2Fapprove-order",
      expected: { page: "login", next: "/orders/ORD-101/approve-order" },
    },
    { name: "an order on its own", path: "/orders/ORD-101", expected: { page: "order", orderId: "ORD-101" } },
    {
      name: "an order task, as hmntsk.route links it",
      path: "/orders/ORD-101/approve-order?task=approve-order-ORD-101",
      expected: { page: "order", orderId: "ORD-101", activity: "approve-order", taskId: "approve-order-ORD-101" },
    },
    {
      name: "escaped path segments are decoded",
      path: "/orders/ORD%2F7%20B/purchase-order",
      expected: { page: "order", orderId: "ORD/7 B", activity: "purchase-order" },
    },
    { name: "too deep is not an order", path: "/orders/ORD-1/review-invoice/extra", expected: { page: "notFound" } },
    {
      name: "a badly escaped path is not found, rather than breaking the page",
      path: "/orders/%E0%A4%A",
      expected: { page: "notFound" },
    },
    { name: "invoices have no page of their own: they are on their order's", path: "/invoices/INV-101", expected: { page: "notFound" } },
    { name: "anything else is not found", path: "/elsewhere", expected: { page: "notFound" } },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const url = new URL(tc.path, "http://demo.test");
      expect(matchPage({ pathname: url.pathname, search: url.search })).toEqual(tc.expected);
    });
  }
});

describe("afterSignIn", () => {
  const approver = { groups: ["finance-approvers"] };
  const purchasing = { groups: ["purchasing"] };

  const cases: { name: string; next?: string; user: { groups: string[] }; expected: string }[] = [
    { name: "an approver starts at the inbox", user: approver, expected: "/" },
    { name: "purchasing starts at orders", user: purchasing, expected: "/orders" },
    {
      name: "the page the viewer opened wins",
      next: "/orders/ORD-101/approve-order?task=t-1",
      user: purchasing,
      expected: "/orders/ORD-101/approve-order?task=t-1",
    },
    { name: "another origin is never a return address", next: "https://evil.example/", user: approver, expected: "/" },
    { name: "a protocol-relative path is another origin", next: "//evil.example/", user: approver, expected: "/" },
    { name: "a backslash path is another origin to some browsers", next: "/\\evil.example", user: approver, expected: "/" },
    { name: "returning to sign-in would loop", next: "/login?next=%2F", user: purchasing, expected: "/orders" },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(afterSignIn(tc.next, tc.user)).toBe(tc.expected));
  }
});

describe("paths", () => {
  it("sign-in carries the page to return to", () => {
    expect(signInPath("/orders/ORD-1/review-invoice?task=t 1")).toBe(
      "/login?next=%2Forders%2FORD-1%2Freview-invoice%3Ftask%3Dt+1",
    );
  });

  it("sign-in from the inbox needs no return address", () => {
    expect(signInPath("/")).toBe("/login");
  });

  it("an order path escapes its segments", () => {
    expect(orderPath("ORD/7 B")).toBe("/orders/ORD%2F7%20B");
  });
});
