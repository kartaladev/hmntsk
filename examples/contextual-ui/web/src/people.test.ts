import { describe, expect, it } from "vitest";

import { initials } from "./people";

describe("initials", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    { name: "first and last name", input: "Erin Walsh", expected: "EW" },
    { name: "a middle name is skipped", input: "Mary Jane Watson", expected: "MW" },
    { name: "one name gives one letter", input: "alice", expected: "A" },
    { name: "extra spaces are ignored", input: "  bob   nakamura ", expected: "BN" },
    { name: "no name at all", input: " ", expected: "?" },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(initials(tc.input)).toBe(tc.expected));
  }
});
