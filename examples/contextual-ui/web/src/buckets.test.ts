import { describe, expect, it } from "vitest";

import { bucketQuery, buckets, taskListQuery } from "./buckets";

describe("buckets", () => {
  const now = new Date("2026-09-14T09:00:00Z");

  it("names each bucket's query for the acting user, as me", () => {
    expect(buckets(now).map((b) => [b.name, bucketQuery(b)])).toEqual([
      ["available", "candidate=me&status=READY"],
      ["mine", "assignee=me&status=RESERVED&status=IN_PROGRESS"],
      ["overdue", "candidate=me&status=READY&status=RESERVED&status=IN_PROGRESS&dueBefore=2026-09-14T09%3A00%3A00.000Z"],
    ]);
  });

  const cases: { name: string; bucket: string; cursor?: string; assert: (query: string) => void }[] = [
    {
      name: "a bucket's task list is ordered by urgency",
      bucket: "available",
      assert: (query) => expect(query).toBe("candidate=me&status=READY&orderBy=urgency&limit=20"),
    },
    {
      name: "the next page continues with the cursor the last page returned",
      bucket: "mine",
      cursor: "abc+/=",
      assert: (query) =>
        expect(query).toBe(
          "assignee=me&status=RESERVED&status=IN_PROGRESS&orderBy=urgency&limit=20&cursor=abc%2B%2F%3D",
        ),
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const bucket = buckets(now).find((b) => b.name === tc.bucket);
      expect(bucket).toBeDefined();
      tc.assert(taskListQuery(bucket!, tc.cursor));
    });
  }
});
