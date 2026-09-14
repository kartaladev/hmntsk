import { describe, expect, it } from "vitest";

import { expandRoute, type RouteTask } from "./route";

// The same cases as TestExpandRoute in the root module's route_test.go, so the
// page links a task exactly as hmntsk.ExpandRoute would.
describe("expandRoute matches hmntsk.ExpandRoute", () => {
  const task: RouteTask = {
    id: "019243af-9f1c-7000-8000-000000000042",
    type: "approval",
    correlation: {
      ownerType: "invoice",
      ownerRef: "INV-42",
      activityKey: "approve",
      extra: { tenant: "acme", path: "eu west/a&b?c" },
    },
  };

  const cases: { name: string; template: string; task: RouteTask; expected: string }[] = [
    {
      name: "a route template expands for a task",
      template: "/invoices/{correlation.ownerRef}/approve?task={task.id}",
      task,
      expected: "/invoices/INV-42/approve?task=019243af-9f1c-7000-8000-000000000042",
    },
    {
      name: "every task and correlation field has a placeholder",
      template: "{task.id}|{task.type}|{correlation.ownerType}|{correlation.ownerRef}|{correlation.activityKey}",
      task,
      expected: "019243af-9f1c-7000-8000-000000000042|approval|invoice|INV-42|approve",
    },
    {
      name: "an extra correlation key has a placeholder of its own",
      template: "https://{correlation.extra.tenant}.example/tasks",
      task,
      expected: "https://acme.example/tasks",
    },
    {
      name: "an unknown placeholder is left alone",
      template: "/{tenant}/tasks/{task.id}",
      task,
      expected: "/{tenant}/tasks/019243af-9f1c-7000-8000-000000000042",
    },
    {
      name: "a missing extra key is left alone",
      template: "/{correlation.extra.region}/{correlation.extra.tenant}",
      task,
      expected: "/{correlation.extra.region}/acme",
    },
    {
      name: "values are inserted raw, because escaping depends on where the template points",
      template: "/files?p={correlation.extra.path}",
      task,
      expected: "/files?p=eu west/a&b?c",
    },
    {
      name: "a placeholder used twice is replaced twice",
      template: "{task.type}/{task.type}",
      task,
      expected: "approval/approval",
    },
    {
      name: "a known field with no value becomes empty",
      template: "/process/{correlation.activityKey}",
      task: { id: "t-1", type: "approval" },
      expected: "/process/",
    },
    {
      name: "a placeholder inside extra braces still expands",
      template: "{{task.type}}",
      task,
      expected: "{approval}",
    },
    {
      name: "an unclosed brace is left alone",
      template: "/tasks/{task.id",
      task,
      expected: "/tasks/{task.id",
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(expandRoute(tc.template, tc.task)).toBe(tc.expected));
  }
});
