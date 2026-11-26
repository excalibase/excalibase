export interface Run<T> {
  key: string;
  items: T[];
}

// Consecutive items with the same key become one run. Items are never moved:
// a list that grows as the admin scrolls only extends its last run or adds one,
// so nothing already on screen jumps.
export function groupRuns<T>(items: readonly T[], keyOf: (item: T) => string): Run<T>[] {
  const runs: Run<T>[] = [];
  for (const item of items) {
    const key = keyOf(item);
    const last = runs.at(-1);
    if (last?.key === key) last.items.push(item);
    else runs.push({ key, items: [item] });
  }
  return runs;
}
