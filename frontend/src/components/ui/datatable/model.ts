export type SortDirection = 'asc' | 'desc';
export interface SortState { columnId: string; direction: SortDirection }
type SortValue = string | number | null | undefined;

// "1–25 of 90" counts from 1; an empty list is 0–0.
export function pageRange(page: number, pageSize: number, total: number): { from: number; to: number } {
  if (total === 0) return { from: 0, to: 0 };
  const from = (page - 1) * pageSize + 1;
  return { from, to: Math.min(page * pageSize, total) };
}

// A new array: the caller's rows are never reordered. Missing values go last either way.
export function sortRows<T>(rows: readonly T[], value: (row: T) => SortValue, direction: SortDirection): T[] {
  const sign = direction === 'asc' ? 1 : -1;
  return [...rows].sort((a, b) => {
    const left = value(a);
    const right = value(b);
    if (left == null && right == null) return 0;
    if (left == null) return 1;
    if (right == null) return -1;
    if (typeof left === 'number' && typeof right === 'number') return sign * (left - right);
    return sign * String(left).localeCompare(String(right), undefined, { numeric: true });
  });
}

// A 403 is "you may not see this", not "there is nothing here"; both differ from an empty list.
export function errorKind(error: unknown): 'forbidden' | 'failed' {
  const status = (error as { response?: { status?: number } } | null)?.response?.status;
  return status === 403 ? 'forbidden' : 'failed';
}

const STORAGE_PREFIX = 'datatable.hiddenColumns.';

// Browser storage can be missing or throw (private window, blocked site data): the table works without it.
export function loadHiddenColumns(storageKey: string | undefined, fallback: readonly string[]): string[] {
  if (!storageKey) return [...fallback];
  try {
    const stored = localStorage.getItem(STORAGE_PREFIX + storageKey);
    if (stored === null) return [...fallback];
    const parsed: unknown = JSON.parse(stored);
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [...fallback];
  } catch {
    return [...fallback];
  }
}

export function saveHiddenColumns(storageKey: string | undefined, hidden: readonly string[]): void {
  if (!storageKey) return;
  try {
    localStorage.setItem(STORAGE_PREFIX + storageKey, JSON.stringify(hidden));
  } catch {
    // The choice then lasts only until the page closes.
  }
}
