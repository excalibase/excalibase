import { useCallback, useMemo, useState } from 'react';
import type { Paging } from '../../ui/DataTable';
import { useDebounced } from '../useDebounced';
import { INITIAL_LIST, PAGE_SIZE, listParams, withFilters, withPage, withSearch, withSort, type ListParams, type ListState } from './listQuery';

const SEARCH_DELAY_MS = 250;

// The state of one server-paged admin table, and the request it makes. Typing
// is followed after a pause so a search asks the server once.
export function useServerList(allowedSorts: readonly string[]): { params: ListParams; paging: (total: number) => Paging } {
  const [state, setState] = useState<ListState>(INITIAL_LIST);
  const search = useDebounced(state.search, SEARCH_DELAY_MS);
  const params = useMemo(() => listParams({ ...state, search }, allowedSorts), [state, search, allowedSorts]);

  const onPageChange = useCallback((page: number) => setState((prev) => withPage(prev, page)), []);
  const onSearchChange = useCallback((text: string) => setState((prev) => withSearch(prev, text)), []);
  const onSortChange = useCallback((sort: ListState['sort']) => setState((prev) => withSort(prev, sort)), []);
  const onFilterChange = useCallback((filters: Record<string, string>) => setState((prev) => withFilters(prev, filters)), []);

  const paging = useCallback(
    (total: number): Paging => ({ mode: 'server', total, page: state.page, pageSize: PAGE_SIZE, onPageChange, onSearchChange, onSortChange, onFilterChange }),
    [state.page, onPageChange, onSearchChange, onSortChange, onFilterChange],
  );
  return { params, paging };
}
