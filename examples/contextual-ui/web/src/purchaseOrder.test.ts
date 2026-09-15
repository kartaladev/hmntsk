import { describe, expect, it } from "vitest";

import { checkUpload, registryEntry } from "./purchaseOrder";

describe("checkUpload", () => {
  const cases: { name: string; file: { name: string; size: number; type: string }; expected: string | undefined }[] = [
    { name: "a PDF is accepted", file: { name: "po.pdf", size: 2048, type: "application/pdf" }, expected: undefined },
    { name: "a scan as JPEG is accepted", file: { name: "po.jpg", size: 2048, type: "image/jpeg" }, expected: undefined },
    { name: "an empty file is refused", file: { name: "po.pdf", size: 0, type: "application/pdf" }, expected: "The file is empty." },
    {
      name: "a file over 5 MB is refused",
      file: { name: "po.pdf", size: 5 * 1024 * 1024 + 1, type: "application/pdf" },
      expected: "A purchase order may be at most 5 MB.",
    },
    {
      name: "anything but a PDF or image is refused",
      file: { name: "po.html", size: 10, type: "text/html" },
      expected: "A purchase order must be a PDF, PNG or JPEG.",
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(checkUpload(tc.file)).toBe(tc.expected));
  }
});

describe("registryEntry", () => {
  const registry = [
    { name: "Acme Paper", contact: "orders@acme-paper.example" },
    { name: "Globex Cloud", contact: "procurement@globex.example" },
  ];

  const cases: { name: string; supplier: string; expected: string | undefined }[] = [
    { name: "a registered supplier", supplier: "Globex Cloud", expected: "procurement@globex.example" },
    { name: "regardless of case and spaces, as the server matches", supplier: "  globex cloud ", expected: "procurement@globex.example" },
    { name: "a supplier not on the registry", supplier: "Stark Industries", expected: undefined },
    { name: "nothing typed yet", supplier: "", expected: undefined },
  ];

  for (const tc of cases) {
    it(tc.name, () => expect(registryEntry(registry, tc.supplier)?.contact).toBe(tc.expected));
  }
});
