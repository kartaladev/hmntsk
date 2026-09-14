// A deliberately minimal JSON Schema form walker, for the demo only. It renders
// the top-level properties of an object schema and hands everything else to a
// JSON text field. Validation is the server's: the output schema is checked in
// full when the task is completed, and the errors come back by JSON Pointer.

export type FieldKind = "string" | "number" | "integer" | "boolean" | "enum" | "json";

export type Field = {
  name: string;
  kind: FieldKind;
  required: boolean;
  options?: string[];
};

type PropertySchema = { type?: unknown; enum?: unknown };

const simpleKinds = new Set<unknown>(["string", "number", "integer", "boolean"]);

export function fieldsFromSchema(schema: unknown): Field[] {
  if (!isObject(schema) || !isObject(schema.properties)) {
    return [];
  }

  const properties = schema.properties as Record<string, PropertySchema>;
  const required = Array.isArray(schema.required) ? schema.required : [];

  return Object.keys(properties)
    .sort()
    .map((name): Field => {
      const property = properties[name] ?? {};
      const field: Field = { name, kind: "json", required: required.includes(name) };

      if (Array.isArray(property.enum) && property.enum.every((option) => typeof option === "string")) {
        field.kind = "enum";
        field.options = property.enum;
      } else if (simpleKinds.has(property.type)) {
        field.kind = property.type as FieldKind;
      }

      return field;
    });
}

// outputFromValues turns what the form holds into the output payload. A field
// with no value is left out rather than sent as "", and a value that cannot be
// converted is reported against its field instead of being sent.
export function outputFromValues(
  fields: Field[],
  values: Record<string, string | boolean>,
): { output: Record<string, unknown>; errors: Record<string, string> } {
  const output: Record<string, unknown> = {};
  const errors: Record<string, string> = {};

  for (const field of fields) {
    const value = values[field.name];

    if (value === undefined || value === "") {
      continue;
    }

    switch (field.kind) {
      case "boolean":
        output[field.name] = value === true || value === "true";
        break;
      case "number":
      case "integer": {
        const n = Number(value);
        const whole = field.kind === "integer";

        if (whole ? Number.isInteger(n) : Number.isFinite(n)) {
          output[field.name] = n;
        } else {
          errors[field.name] = whole ? "must be a whole number" : "must be a number";
        }
        break;
      }
      case "json":
        try {
          output[field.name] = JSON.parse(String(value));
        } catch {
          errors[field.name] = "must be valid JSON";
        }
        break;
      default:
        output[field.name] = String(value);
    }
  }

  return { output, errors };
}

// withBooleanDefaults gives every switch the user never touched the value it
// shows, false. It belongs to submitting a form, not to reading one: a switch
// always displays a value, so completing means "no" for an untouched one, while
// saving progress should record only what the user actually set.
export function withBooleanDefaults(
  fields: Field[],
  values: Record<string, string | boolean>,
): Record<string, string | boolean> {
  const defaults = Object.fromEntries(
    fields.filter((field) => field.kind === "boolean").map((field) => [field.name, false]),
  );

  return { ...defaults, ...values };
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
