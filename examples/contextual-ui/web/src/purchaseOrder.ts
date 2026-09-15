import type { Supplier } from "./api";

// What the server accepts as an uploaded purchase order. The server decides by
// the file's content, not by the type the browser reports; checking here only
// saves the viewer a round trip for a file that would be refused.
const uploadTypes = ["application/pdf", "image/png", "image/jpeg"];
const maxUploadBytes = 5 * 1024 * 1024;

export const uploadAccept = uploadTypes.join(",");

// checkUpload is why a chosen file cannot be uploaded, or undefined when it can.
export function checkUpload(file: { name: string; size: number; type: string }): string | undefined {
  if (file.size === 0) {
    return "The file is empty.";
  }

  if (file.size > maxUploadBytes) {
    return "A purchase order may be at most 5 MB.";
  }

  if (!uploadTypes.includes(file.type)) {
    return "A purchase order must be a PDF, PNG or JPEG.";
  }

  return undefined;
}

// registryEntry is the registry's entry for a supplier name, matched regardless
// of case and surrounding spaces, as the server matches it.
export function registryEntry(registry: Supplier[], supplier: string): Supplier | undefined {
  const name = supplier.trim().toLowerCase();

  return name ? registry.find((entry) => entry.name.toLowerCase() === name) : undefined;
}
