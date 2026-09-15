// A data grid pages by number; the task API pages by cursor, each page naming
// where the next one starts. These keep the cursor that starts each page the
// viewer has reached, so the grid can ask the API for a page by its number.

// cursorFor is the cursor that starts page: none for the first page, and null
// for a page the viewer has not reached, which cannot be asked for yet.
export function cursorFor(cursors: (string | undefined)[], page: number): string | undefined | null {
  if (page === 0) {
    return undefined;
  }

  if (page < 0 || page >= cursors.length) {
    return null;
  }

  return cursors[page];
}

// rememberNext records where the page after page starts, as that page's answer
// said. Cursors beyond it are forgotten: they were computed from the list as it
// was before, which this read may have found changed.
export function rememberNext(
  cursors: (string | undefined)[],
  page: number,
  next: string | undefined,
): (string | undefined)[] {
  const kept = cursors.slice(0, page + 1);

  while (kept.length < page + 1) {
    kept.push(undefined);
  }

  return next === undefined ? kept : [...kept, next];
}
