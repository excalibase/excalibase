import { useId, useMemo, useState, type ReactNode } from 'react';
import { AlertTriangle, ArrowDown, ArrowUp, ChevronLeft, ChevronRight, Loader2, Search, X } from 'lucide-react';
import { cn } from '../../utils/cn';
import { errorKind, loadHiddenColumns, pageRange, saveHiddenColumns, sortRows, type SortState } from './datatable/model';
import { ColumnChooser } from './datatable/ColumnChooser';
import { useRowSelection } from './datatable/useRowSelection';

export interface Column<T> {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  // Makes the column sortable.
  sortValue?: (row: T) => string | number | null | undefined;
  // What the text filter searches.
  text?: (row: T) => string;
  defaultHidden?: boolean;
  // Cannot be hidden from the column chooser.
  locked?: boolean;
}

export interface FilterDef<T> {
  id: string;
  label: string;
  options: readonly { value: string; label: string }[];
  matches: (row: T, value: string) => boolean;
}

// Client: the table searches, filters, sorts and pages the rows it is given.
// Server: `rows` is already the current page; the table only reports what the user asked for.
export type Paging =
  | { mode: 'client'; pageSize?: number }
  | {
      mode: 'server';
      total: number;
      page: number;
      pageSize: number;
      onPageChange: (page: number) => void;
      onSortChange?: (sort: SortState | null) => void;
      onSearchChange?: (text: string) => void;
      // Active filters by filter id; a cleared filter is absent.
      onFilterChange?: (active: Record<string, string>) => void;
    };

export type { SortState };

export interface DataTableProps<T> {
  readonly label: string;
  readonly columns: readonly Column<T>[];
  readonly rows: readonly T[];
  readonly getRowId: (row: T) => string;
  readonly isLoading?: boolean;
  readonly error?: unknown;
  readonly onRetry?: () => void;
  readonly emptyState?: ReactNode;
  readonly filters?: readonly FilterDef<T>[];
  readonly paging?: Paging;
  readonly selectable?: boolean;
  readonly bulkActions?: (selected: T[], clear: () => void) => ReactNode;
  // Remembers the column choice in this browser.
  readonly storageKey?: string;
  readonly toolbarActions?: ReactNode;
  // Adds a trailing Actions column; keep it to buttons and links.
  readonly rowActions?: (row: T) => ReactNode;
}

const PAGE_SIZES = [10, 25, 50, 100];
// On a phone a row stacks into a card: each cell is a line headed by its column's name.
const CELL = 'px-4 py-3 max-md:flex max-md:items-baseline max-md:justify-between max-md:gap-4 max-md:px-0 max-md:py-1.5 max-md:before:shrink-0 max-md:before:text-xs max-md:before:text-text-tertiary max-md:before:content-[attr(data-label)]';
const FIELD = 'bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary';

export function DataTable<T>(props: DataTableProps<T>) {
  const { label, columns, rows, getRowId, isLoading, error, onRetry, emptyState, filters = [], paging, selectable, bulkActions, storageKey, toolbarActions, rowActions } = props;
  const [text, setText] = useState('');
  const [active, setActive] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<SortState | null>(null);
  const [page, setPage] = useState(1);
  const [clientSize, setClientSize] = useState(paging?.mode === 'client' ? (paging.pageSize ?? 25) : 25);
  const [hidden, setHidden] = useState(() => loadHiddenColumns(storageKey, columns.filter((c) => c.defaultHidden).map((c) => c.id)));
  const server = paging?.mode === 'server';

  const visible = columns.filter((c) => !hidden.includes(c.id));
  const filtered = useMemo(() => (server ? rows : filterRows(rows, columns, filters, text, active)), [server, rows, columns, filters, text, active]);
  const sorted = useMemo(() => {
    const column = columns.find((c) => c.id === sort?.columnId);
    return server || !sort || !column?.sortValue ? filtered : sortRows(filtered, column.sortValue, sort.direction);
  }, [server, filtered, sort, columns]);

  const total = server ? paging.total : sorted.length;
  const pageSize = server ? paging.pageSize : clientSize;
  const current = server ? paging.page : Math.min(page, Math.max(1, Math.ceil(total / pageSize)));
  const shown = server ? rows : sorted.slice((current - 1) * pageSize, current * pageSize);
  const selection = useRowSelection(shown, getRowId);
  const filtering = text !== '' || Object.keys(active).length > 0;

  const changeFilter = (next: () => void) => { next(); setPage(1); selection.clear(); };
  const changeText = (value: string) => { changeFilter(() => setText(value)); if (server) paging.onSearchChange?.(value); };
  const changeActive = (id: string, value: string) => {
    changeFilter(() => setActive((prev) => withValue(prev, id, value)));
    if (server) paging.onFilterChange?.(withValue(active, id, value));
  };
  const clearAll = () => {
    changeFilter(() => { setText(''); setActive({}); });
    if (server) { paging.onSearchChange?.(''); paging.onFilterChange?.({}); }
  };
  const toggleColumn = (id: string) => {
    const next = hidden.includes(id) ? hidden.filter((h) => h !== id) : [...hidden, id];
    setHidden(next);
    saveHiddenColumns(storageKey, next);
  };
  const toggleSort = (id: string) => {
    const next: SortState = sort?.columnId !== id ? { columnId: id, direction: 'asc' } : { columnId: id, direction: sort.direction === 'asc' ? 'desc' : 'asc' };
    setSort(next);
    selection.clear();
    if (server) paging.onSortChange?.(next);
  };

  if (error) return <ErrorPanel label={label} error={error} onRetry={onRetry} />;

  return (
    <section aria-label={label} className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <FilterInput label={label} value={text} onChange={changeText} />
        {filters.map((filter) => (
          <FilterSelect key={filter.id} filter={filter} value={active[filter.id] ?? ''}
            onChange={(value) => changeActive(filter.id, value)} />
        ))}
        <div className="ml-auto flex items-center gap-2">
          {toolbarActions}
          <ColumnChooser columns={columns} hidden={hidden} onToggle={toggleColumn} />
        </div>
      </div>
      <ActiveChips filters={filters} active={active} onRemove={(id) => changeActive(id, '')} />
      {selectable && selection.count > 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-purple-500/30 bg-purple-500/10 px-3 py-2 text-sm">
          <span>{selection.count} selected</span>
          {bulkActions?.(selection.selected, selection.clear)}
          <button type="button" onClick={selection.clear} className="ml-auto text-text-secondary hover:text-text-primary">Clear selection</button>
        </div>
      )}
      <div className="bg-surface-card border border-border-primary rounded-xl overflow-x-auto">
        {isLoading ? (
          <output className="flex items-center justify-center gap-2 py-16 text-sm text-text-secondary">
            <Loader2 className="w-4 h-4 animate-spin" /> Loading {label.toLowerCase()}
          </output>
        ) : (
          <Body label={label} visible={visible} shown={shown} getRowId={getRowId} selectable={selectable} selection={selection}
            sort={sort} onSort={toggleSort} empty={total === 0} filtering={filtering} emptyState={emptyState}
            onClear={clearAll} rowActions={rowActions} />
        )}
      </div>
      {!isLoading && total > 0 && (
        <Pager range={pageRange(current, pageSize, total)} total={total} page={current} pageSize={pageSize}
          hasNext={current * pageSize < total} sizeChangeable={!server}
          onPage={(next) => { selection.clear(); if (server) paging.onPageChange(next); else setPage(next); }}
          onSize={(size) => { setClientSize(size); setPage(1); selection.clear(); }} />
      )}
    </section>
  );
}

function withValue(prev: Record<string, string>, id: string, value: string): Record<string, string> {
  const { [id]: _removed, ...rest } = prev;
  return value === '' ? rest : { ...rest, [id]: value };
}

function filterRows<T>(rows: readonly T[], columns: readonly Column<T>[], filters: readonly FilterDef<T>[], text: string, active: Record<string, string>): T[] {
  const needle = text.trim().toLowerCase();
  return rows.filter((row) => {
    const matchesText = needle === '' || columns.some((c) => c.text?.(row).toLowerCase().includes(needle));
    return matchesText && filters.every((f) => !active[f.id] || f.matches(row, active[f.id]));
  });
}

function ErrorPanel({ label, error, onRetry }: { readonly label: string; readonly error: unknown; readonly onRetry?: () => void }) {
  const forbidden = errorKind(error) === 'forbidden';
  return (
    <div role="alert" className="rounded-xl border border-red-500/30 bg-red-500/10 p-6 text-sm">
      <p className="flex items-center gap-2 font-medium text-red-400">
        <AlertTriangle className="w-4 h-4" /> Could not load {label.toLowerCase()}
      </p>
      <p className="mt-1 text-text-secondary">
        {forbidden ? 'You do not have access to this list. Ask an organization owner for access.' : 'Something went wrong while loading. Nothing here means nothing was loaded, not that the list is empty.'}
      </p>
      {onRetry && !forbidden && (
        <button type="button" onClick={onRetry} className="mt-3 rounded-lg border border-border-primary px-3 py-1.5 text-text-primary hover:bg-surface-hover">Try again</button>
      )}
    </div>
  );
}

function FilterInput({ label, value, onChange }: { readonly label: string; readonly value: string; readonly onChange: (v: string) => void }) {
  return (
    <label className={cn(FIELD, 'flex items-center gap-2 px-3 min-h-9 min-w-[220px]')}>
      <Search className="w-4 h-4 text-text-tertiary" aria-hidden="true" />
      <input type="search" role="searchbox" aria-label={`Filter ${label.toLowerCase()}`} placeholder={`Filter ${label.toLowerCase()}`} value={value}
        onChange={(e) => onChange(e.target.value)} className="flex-1 bg-transparent outline-none placeholder:text-text-tertiary" />
    </label>
  );
}

function FilterSelect<T>({ filter, value, onChange }: { readonly filter: FilterDef<T>; readonly value: string; readonly onChange: (v: string) => void }) {
  return (
    <select aria-label={filter.label} value={value} onChange={(e) => onChange(e.target.value)} className={cn(FIELD, 'min-h-9 px-2')}>
      <option value="">{filter.label}: all</option>
      {filter.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );
}

function ActiveChips<T>({ filters, active, onRemove }: { readonly filters: readonly FilterDef<T>[]; readonly active: Record<string, string>; readonly onRemove: (id: string) => void }) {
  const chips = filters.filter((f) => active[f.id]);
  if (chips.length === 0) return null;
  return (
    <ul className="flex flex-wrap gap-2" aria-label="Active filters">
      {chips.map((f) => {
        const text = `${f.label}: ${f.options.find((o) => o.value === active[f.id])?.label ?? active[f.id]}`;
        return (
          <li key={f.id} className="inline-flex items-center gap-1 rounded-full border border-border-primary bg-bg-secondary px-2.5 py-1 text-xs">
            {text}
            <button type="button" aria-label={`Remove filter ${text}`} onClick={() => onRemove(f.id)}><X className="w-3 h-3" /></button>
          </li>
        );
      })}
    </ul>
  );
}

interface BodyProps<T> {
  readonly label: string; readonly visible: readonly Column<T>[]; readonly shown: readonly T[]; readonly getRowId: (row: T) => string;
  readonly selectable?: boolean; readonly selection: ReturnType<typeof useRowSelection<T>>; readonly sort: SortState | null;
  readonly onSort: (id: string) => void; readonly empty: boolean; readonly filtering: boolean; readonly emptyState?: ReactNode; readonly onClear: () => void;
  readonly rowActions?: (row: T) => ReactNode;
}

function Body<T>(p: BodyProps<T>) {
  const captionId = useId();
  if (p.empty) {
    return p.filtering ? (
      <div className="py-12 text-center text-sm text-text-secondary">
        <p>No {p.label.toLowerCase()} match your filters.</p>
        <button type="button" onClick={p.onClear} className="mt-2 text-purple-400 hover:underline">Clear filters</button>
      </div>
    ) : (
      <div className="py-12 text-center text-sm text-text-secondary">{p.emptyState ?? `No ${p.label.toLowerCase()}.`}</div>
    );
  }
  return (
    <table className="w-full text-sm max-md:block" aria-labelledby={captionId}>
      <caption id={captionId} className="sr-only">{p.label}</caption>
      <thead className="max-md:sr-only">
        <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
          {p.selectable && (
            <th scope="col" className="w-10 px-4 py-3">
              <input type="checkbox" aria-label="Select all rows on this page" checked={p.selection.allSelected}
                ref={(el) => { if (el) el.indeterminate = p.selection.count > 0 && !p.selection.allSelected; }} onChange={p.selection.toggleAll} />
            </th>
          )}
          {p.visible.map((c) => <HeaderCell key={c.id} column={c} sort={p.sort} onSort={p.onSort} />)}
          {p.rowActions && <th scope="col" className="text-right px-4 py-3 font-medium">Actions</th>}
        </tr>
      </thead>
      <tbody className="max-md:block">
        {p.shown.map((row) => {
          const id = p.getRowId(row);
          return (
            <tr key={id} className="border-b border-border-primary last:border-0 hover:bg-surface-hover max-md:block max-md:px-4 max-md:py-3">
              {p.selectable && (
                <td className="px-4 py-3">
                  <input type="checkbox" aria-label={`Select ${rowName(p.visible, row, id)}`} checked={p.selection.isSelected(id)} onChange={() => p.selection.toggle(id)} />
                </td>
              )}
              {p.visible.map((c) => <td key={c.id} data-label={c.header} className={CELL}>{c.cell(row)}</td>)}
              {p.rowActions && <td className={cn(CELL, 'text-right max-md:justify-end')}>{p.rowActions(row)}</td>}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function rowName<T>(visible: readonly Column<T>[], row: T, fallback: string): string {
  return visible[0]?.text?.(row) ?? fallback;
}

function ariaSortOf(direction: 'asc' | 'desc' | null, sortable: boolean): 'ascending' | 'descending' | 'none' | undefined {
  if (direction === 'asc') return 'ascending';
  if (direction === 'desc') return 'descending';
  return sortable ? 'none' : undefined;
}

function HeaderCell<T>({ column, sort, onSort }: { readonly column: Column<T>; readonly sort: SortState | null; readonly onSort: (id: string) => void }) {
  const direction = sort?.columnId === column.id ? sort.direction : null;
  const ariaSort = ariaSortOf(direction, Boolean(column.sortValue));
  return (
    <th scope="col" aria-sort={ariaSort} className="text-left px-4 py-3 font-medium">
      {column.sortValue ? (
        <button type="button" onClick={() => onSort(column.id)} className="inline-flex items-center gap-1 hover:text-text-primary">
          {column.header}
          {direction === 'asc' && <ArrowUp className="w-3 h-3" aria-hidden="true" />}
          {direction === 'desc' && <ArrowDown className="w-3 h-3" aria-hidden="true" />}
        </button>
      ) : column.header}
    </th>
  );
}

interface PagerProps { readonly range: { from: number; to: number }; readonly total: number; readonly page: number; readonly pageSize: number; readonly hasNext: boolean; readonly sizeChangeable: boolean; readonly onPage: (page: number) => void; readonly onSize: (size: number) => void }

function Pager(p: PagerProps) {
  const sizeId = useId();
  return (
    <div className="flex flex-wrap items-center gap-3 text-sm text-text-secondary">
      <span aria-live="polite">{p.range.from}–{p.range.to} of {p.total}</span>
      {p.sizeChangeable && (
        <label htmlFor={sizeId} className="flex items-center gap-1">
          <span>Rows per page</span>
          <select id={sizeId} value={p.pageSize} onChange={(e) => p.onSize(Number(e.target.value))} className={cn(FIELD, 'px-1 py-0.5')}>
            {PAGE_SIZES.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </label>
      )}
      <div className="ml-auto flex gap-1">
        <button type="button" aria-label="Previous page" disabled={p.page <= 1} onClick={() => p.onPage(p.page - 1)} className="rounded-lg border border-border-primary p-1.5 disabled:opacity-40"><ChevronLeft className="w-4 h-4" /></button>
        <button type="button" aria-label="Next page" disabled={!p.hasNext} onClick={() => p.onPage(p.page + 1)} className="rounded-lg border border-border-primary p-1.5 disabled:opacity-40"><ChevronRight className="w-4 h-4" /></button>
      </div>
    </div>
  );
}

