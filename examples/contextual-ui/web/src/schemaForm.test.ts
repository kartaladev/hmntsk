import { describe, expect, it } from "vitest";

import { fieldsFromSchema, outputFromValues, withBooleanDefaults, type Field } from "./schemaForm";

describe("withBooleanDefaults", () => {
  const fields: Field[] = [
    { name: "approved", kind: "boolean", required: true },
    { name: "urgent", kind: "boolean", required: false },
    { name: "note", kind: "string", required: false },
  ];

  const cases: { name: string; values: Record<string, string | boolean>; expected: Record<string, string | boolean> }[] = [
    {
      // A switch always shows a value, so submitting a form means "no" for one
      // the user never touched.
      name: "a switch the user never touched is false",
      values: { note: "ok" },
      expected: { approved: false, urgent: false, note: "ok" },
    },
    {
      name: "a switch the user set keeps its value",
      values: { approved: true },
      expected: { approved: true, urgent: false },
    },
    {
      name: "fields of other kinds get no default",
      values: {},
      expected: { approved: false, urgent: false },
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(withBooleanDefaults(fields, tc.values)).toEqual(tc.expected));
  }
});

describe("fieldsFromSchema", () => {
  const cases: { name: string; schema: unknown; assert: (fields: Field[]) => void }[] = [
    {
      name: "reads each top-level property, sorted, with its kind and whether it is required",
      schema: {
        type: "object",
        properties: {
          reason: { type: "string", enum: ["within-budget", "duplicate"] },
          approved: { type: "boolean" },
          note: { type: "string" },
          amount: { type: "number" },
          count: { type: "integer" },
        },
        required: ["approved", "reason"],
      },
      assert: (fields) =>
        expect(fields).toEqual([
          { name: "amount", kind: "number", required: false },
          { name: "approved", kind: "boolean", required: true },
          { name: "count", kind: "integer", required: false },
          { name: "note", kind: "string", required: false },
          { name: "reason", kind: "enum", required: true, options: ["within-budget", "duplicate"] },
        ]),
    },
    {
      name: "anything the walker does not render becomes a JSON field",
      schema: {
        type: "object",
        properties: {
          lines: { type: "array", items: { type: "string" } },
          address: { type: "object" },
          anything: {},
        },
      },
      assert: (fields) =>
        expect(fields.map((f) => [f.name, f.kind])).toEqual([
          ["address", "json"],
          ["anything", "json"],
          ["lines", "json"],
        ]),
    },
    {
      name: "a schema with no properties has no fields",
      schema: { type: "object" },
      assert: (fields) => expect(fields).toEqual([]),
    },
    {
      name: "no schema at all has no fields",
      schema: undefined,
      assert: (fields) => expect(fields).toEqual([]),
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => tc.assert(fieldsFromSchema(tc.schema)));
  }
});

describe("outputFromValues", () => {
  const fields: Field[] = [
    { name: "amount", kind: "number", required: false },
    { name: "approved", kind: "boolean", required: true },
    { name: "count", kind: "integer", required: false },
    { name: "extra", kind: "json", required: false },
    { name: "note", kind: "string", required: false },
    { name: "reason", kind: "enum", required: true, options: ["within-budget", "duplicate"] },
  ];

  const cases: {
    name: string;
    values: Record<string, string | boolean>;
    assert: (result: ReturnType<typeof outputFromValues>) => void;
  }[] = [
    {
      name: "converts each value to its field's kind and leaves out empty optional fields",
      values: { approved: true, reason: "duplicate", amount: "12.5", count: "3", note: "", extra: "" },
      assert: (result) => {
        expect(result.errors).toEqual({});
        expect(result.output).toEqual({ approved: true, reason: "duplicate", amount: 12.5, count: 3 });
      },
    },
    {
      name: "a false boolean is a value, not an empty field",
      values: { approved: false, reason: "within-budget" },
      assert: (result) => expect(result.output).toEqual({ approved: false, reason: "within-budget" }),
    },
    {
      name: "a boolean with no value is left out like any other field",
      values: { reason: "duplicate" },
      assert: (result) => {
        expect(result.errors).toEqual({});
        expect(result.output).toEqual({ reason: "duplicate" });
      },
    },
    {
      name: "parses a JSON field",
      values: { approved: true, reason: "duplicate", extra: '{"po":"PO-1"}' },
      assert: (result) => expect(result.output.extra).toEqual({ po: "PO-1" }),
    },
    {
      name: "reports what cannot be converted, by field",
      values: { approved: true, reason: "duplicate", amount: "lots", count: "1.5", extra: "{nope" },
      assert: (result) =>
        expect(Object.keys(result.errors).sort()).toEqual(["amount", "count", "extra"]),
    },
    {
      name: "a number holding only spaces is left out like an empty one, not sent as zero",
      values: { approved: true, reason: "duplicate", amount: "  ", count: " " },
      assert: (result) => {
        expect(result.errors).toEqual({});
        expect(result.output).toEqual({ approved: true, reason: "duplicate" });
      },
    },
    {
      name: "leaves a missing required value to the server's validation",
      values: { approved: true },
      assert: (result) => {
        expect(result.errors).toEqual({});
        expect(result.output).toEqual({ approved: true });
      },
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => tc.assert(outputFromValues(fields, tc.values)));
  }
});
