import { describe, expect, it } from "vitest";

import { afterSignIn, invoicePath, matchPage, signInPath, type PageRoute } from "./pages";

describe("matchPage", () => {
  const cases: { name: string; path: string; expected: PageRoute }[] = [
    { name: "the root is the inbox", path: "/", expected: { page: "inbox" } },
    { name: "orders", path: "/orders", expected: { page: "orders" } },
    { name: "a trailing slash names the same page", path: "/orders/", expected: { page: "orders" } },
    { name: "sign-in with nowhere to return to", path: "/login", expected: { page: "login" } },
    {
      name: "sign-in remembers where to return",
      path: "/login?next=%2Finvoices%2FINV-101%2Fapprove",
      expected: { page: "login", next: "/invoices/INV-101/approve" },
    },
    { name: "an invoice on its own", path: "/invoices/INV-101", expected: { page: "invoice", invoiceId: "INV-101" } },
    {
      name: "an invoice task, as hmntsk.route links it",
      path: "/invoices/INV-101/approve?task=019243af-0001",
      expected: { page: "invoice", invoiceId: "INV-101", activity: "approve", taskId: "019243af-0001" },
    },
    {
      name: "escaped path segments are decoded",
      path: "/invoices/INV%2F7%20B/review",
      expected: { page: "invoice", invoiceId: "INV/7 B", activity: "review" },
    },
    { name: "an invoice needs an ID", path: "/invoices/", expected: { page: "notFound" } },
    { name: "too deep is not an invoice", path: "/invoices/INV-1/review/extra", expected: { page: "notFound" } },
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
      next: "/invoices/INV-101/approve?task=t-1",
      user: purchasing,
      expected: "/invoices/INV-101/approve?task=t-1",
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
    expect(signInPath("/invoices/INV-1/review?task=t 1")).toBe("/login?next=%2Finvoices%2FINV-1%2Freview%3Ftask%3Dt+1");
  });

  it("sign-in from the inbox needs no return address", () => {
    expect(signInPath("/")).toBe("/login");
  });

  it("an invoice path escapes its segments", () => {
    expect(invoicePath("INV/7 B")).toBe("/invoices/INV%2F7%20B");
  });
});
