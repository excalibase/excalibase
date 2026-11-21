import { useEffect, useState } from 'react';

// Selection by row id, so it survives sorting; it covers the page that is shown.
// An id whose row is no longer shown (a refetch dropped it) is forgotten, so the
// count is always the number of rows handed to the bulk actions.
export function useRowSelection<T>(shown: readonly T[], getRowId: (row: T) => string) {
  const [ids, setIds] = useState<ReadonlySet<string>>(new Set());
  const onPage = shown.map(getRowId);
  const selected = shown.filter((row) => ids.has(getRowId(row)));
  const stale = ids.size !== selected.length;
  useEffect(() => {
    if (stale) setIds(new Set(selected.map(getRowId)));
  }, [stale, selected, getRowId]);
  const allSelected = onPage.length > 0 && onPage.every((id) => ids.has(id));
  return {
    count: selected.length,
    selected,
    allSelected,
    isSelected: (id: string) => ids.has(id),
    toggle: (id: string) => setIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    }),
    toggleAll: () => setIds((prev) => {
      const next = new Set(prev);
      for (const id of onPage) { if (allSelected) next.delete(id); else next.add(id); }
      return next;
    }),
    clear: () => setIds(new Set()),
  };
}
