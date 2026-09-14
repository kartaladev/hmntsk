// initials are what an avatar shows: the first letters of the first and last
// names.
export function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) {
    return "?";
  }

  const first = words[0]!;
  const last = words.length > 1 ? words[words.length - 1]! : "";

  return (first[0]! + (last[0] ?? "")).toUpperCase();
}
