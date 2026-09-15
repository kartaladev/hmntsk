import { describe, expect, it } from "vitest";

import { cursorFor, rememberNext } from "./paging";

describe("cursorFor", () => {
  const cases: { name: string; cursors: (string | undefined)[]; page: number; expected: string | undefined | null }[] = [
    { name: "the first page needs no cursor", cursors: [], page: 0, expected: undefined },
    { name: "a page reached before uses the cursor that started it", cursors: [undefined, "c1", "c2"], page: 2, expected: "c2" },
    { name: "a page not yet reached cannot be asked for", cursors: [undefined, "c1"], page: 3, expected: null },
    { name: "a negative page is no page", cursors: [undefined], page: -1, expected: null },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(cursorFor(tc.cursors, tc.page)).toBe(tc.expected));
  }
});

describe("rememberNext", () => {
  const cases: {
    name: string;
    cursors: (string | undefined)[];
    page: number;
    next: string | undefined;
    expected: (string | undefined)[];
  }[] = [
    { name: "the first page's next cursor starts the second", cursors: [], page: 0, next: "c1", expected: [undefined, "c1"] },
    {
      name: "re-reading a page forgets the cursors after it, which were computed from a list that changed",
      cursors: [undefined, "c1", "c2", "c3"],
      page: 1,
      next: "c2b",
      expected: [undefined, "c1", "c2b"],
    },
    { name: "the last page has no next", cursors: [undefined, "c1"], page: 1, next: undefined, expected: [undefined, "c1"] },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const before = [...tc.cursors];
      expect(rememberNext(tc.cursors, tc.page, tc.next)).toEqual(tc.expected);
      expect(tc.cursors).toEqual(before);
    });
  }
});
