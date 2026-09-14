import { describe, expect, it } from "vitest";

import { ApiError, isStale } from "./api";

describe("isStale", () => {
  const cases: { name: string; error: unknown; expected: boolean }[] = [
    {
      name: "a conflict means the task changed since the page read it",
      error: new ApiError(409, "conflict", "the task was modified"),
      expected: true,
    },
    {
      name: "a refused operation is not a stale task",
      error: new ApiError(403, "forbidden", "not a participant"),
      expected: false,
    },
    {
      name: "invalid output is not a stale task",
      error: new ApiError(400, "invalid_output", "reason is required"),
      expected: false,
    },
    {
      name: "an error that is not the API's answer is not a stale task",
      error: new Error("network down"),
      expected: false,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(isStale(tc.error)).toBe(tc.expected));
  }
});
