import type { SortState } from '../../ui/DataTable';

// What a server-paged admin table asks for: the state the reader changed, kept
// apart from the request it becomes so the rules are testable on their own.
export interface ListState {
  page: number;
  search: string;
  sort: SortState | null;
  filters: Readonly<Record<string, string>>;
}

export const PAGE_SIZE = 25;

export const INITIAL_LIST: ListState = { page: 1, search: '', sort: null, filters: {} };

// The columns each list may be sorted by; the same words the server allows.
export const PROJECT_SORTS: readonly string[] = ['name', 'org', 'owner', 'plan', 'status', 'created'];
export const USER_SORTS: readonly string[] = ['created', 'email', 'lastLogin', 'orgs', 'projects', 'apps'];
export const ORG_SORTS: readonly string[] = ['created', 'name', 'owner', 'plan', 'projects', 'apps'];

export type ListParams = Record<string, string | number>;

// Page 1 is the first page; the request counts rows from 0.
export function listParams(state: ListState, allowedSorts: readonly string[]): ListParams {
  const params: ListParams = { limit: PAGE_SIZE, offset: (state.page - 1) * PAGE_SIZE };
  const search = state.search.trim();
  if (search) params.q = search;
  if (state.sort && allowedSorts.includes(state.sort.columnId)) {
    params.sort = state.sort.columnId;
    params.order = state.sort.direction;
  }
  return { ...params, ...state.filters };
}

export const withPage = (state: ListState, page: number): ListState => ({ ...state, page });
export const withSearch = (state: ListState, search: string): ListState => ({ ...state, search, page: 1 });
export const withSort = (state: ListState, sort: SortState | null): ListState => ({ ...state, sort, page: 1 });
export const withFilters = (state: ListState, filters: Record<string, string>): ListState => ({ ...state, filters: { ...filters }, page: 1 });
