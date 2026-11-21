import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DataTable, type Column, type FilterDef } from './DataTable';

interface Row { id: string; name: string; plan: string; size: number }

const COLUMNS: Column<Row>[] = [
  { id: 'name', header: 'Name', cell: (r) => r.name, sortValue: (r) => r.name, text: (r) => r.name, locked: true },
  { id: 'plan', header: 'Plan', cell: (r) => r.plan, sortValue: (r) => r.plan, text: (r) => r.plan },
  { id: 'size', header: 'Size', cell: (r) => String(r.size), sortValue: (r) => r.size },
];

const PLAN_FILTER: FilterDef<Row> = {
  id: 'plan',
  label: 'Plan',
  options: [{ value: 'FREE', label: 'Free' }, { value: 'STANDARD', label: 'Standard' }],
  matches: (r, v) => r.plan === v,
};

function makeRows(n: number): Row[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `r${i + 1}`,
    name: `proj-${String(i + 1).padStart(2, '0')}`,
    plan: i % 2 === 0 ? 'FREE' : 'STANDARD',
    size: (i * 7) % 10,
  }));
}

function names(): string[] {
  return screen.getAllByRole('row').slice(1).map((row) => within(row).getAllByRole('cell')[0].textContent ?? '');
}

describe('DataTable', () => {
  beforeEach(() => localStorage.clear());

  test('filters rows by text', async () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(5)} getRowId={(r) => r.id} />);
    await userEvent.type(screen.getByRole('searchbox', { name: /filter projects/i }), 'proj-03');
    expect(names()).toEqual(['proj-03']);
  });

  test('a filter dropdown narrows rows and shows a removable chip', async () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(4)} getRowId={(r) => r.id} filters={[PLAN_FILTER]} />);
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Plan' }), 'STANDARD');
    expect(names()).toEqual(['proj-02', 'proj-04']);
    await userEvent.click(screen.getByRole('button', { name: /remove filter plan: standard/i }));
    expect(names()).toHaveLength(4);
  });

  test('sorts by a column, ascending then descending', async () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(3)} getRowId={(r) => r.id} />);
    const header = screen.getByRole('columnheader', { name: /name/i });
    await userEvent.click(within(header).getByRole('button'));
    expect(header).toHaveAttribute('aria-sort', 'ascending');
    await userEvent.click(within(header).getByRole('button'));
    expect(header).toHaveAttribute('aria-sort', 'descending');
    expect(names()).toEqual(['proj-03', 'proj-02', 'proj-01']);
  });

  test('pages on the client with a 1-25 of N range', async () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(60)} getRowId={(r) => r.id} />);
    expect(screen.getByText('1–25 of 60')).toBeInTheDocument();
    expect(names()).toHaveLength(25);
    await userEvent.click(screen.getByRole('button', { name: /next page/i }));
    expect(screen.getByText('26–50 of 60')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /next page/i }));
    expect(screen.getByText('51–60 of 60')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /next page/i })).toBeDisabled();
  });

  test('server paging shows the server total and asks for the page', async () => {
    const onPageChange = vi.fn();
    render(
      <DataTable
        label="Projects"
        columns={COLUMNS}
        rows={makeRows(25)}
        getRowId={(r) => r.id}
        paging={{ mode: 'server', total: 90, page: 1, pageSize: 25, onPageChange }}
      />,
    );
    expect(screen.getByText('1–25 of 90')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /next page/i }));
    expect(onPageChange).toHaveBeenCalledWith(2);
    expect(names()).toHaveLength(25);
  });

  test('a failure is an error, never "no items"', () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={[]} getRowId={(r) => r.id} error={new Error('boom')} emptyState={<p>No projects yet</p>} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/could not load projects/i);
    expect(screen.queryByText('No projects yet')).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  test('a 403 says the caller has no access', () => {
    const forbidden = { isAxiosError: true, response: { status: 403 } };
    render(<DataTable label="Projects" columns={COLUMNS} rows={[]} getRowId={(r) => r.id} error={forbidden} emptyState={<p>No projects yet</p>} />);
    expect(screen.getByRole('alert')).toHaveTextContent(/do not have access/i);
    expect(screen.queryByText('No projects yet')).not.toBeInTheDocument();
  });

  test('the error offers a retry', async () => {
    const onRetry = vi.fn();
    render(<DataTable label="Projects" columns={COLUMNS} rows={[]} getRowId={(r) => r.id} error={new Error('x')} onRetry={onRetry} />);
    await userEvent.click(screen.getByRole('button', { name: /try again/i }));
    expect(onRetry).toHaveBeenCalled();
  });

  test('an empty list shows the empty state, and no match offers to clear the filter', async () => {
    const { rerender } = render(<DataTable label="Projects" columns={COLUMNS} rows={[]} getRowId={(r) => r.id} emptyState={<p>No projects yet</p>} />);
    expect(screen.getByText('No projects yet')).toBeInTheDocument();
    rerender(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(3)} getRowId={(r) => r.id} emptyState={<p>No projects yet</p>} />);
    await userEvent.type(screen.getByRole('searchbox'), 'zzz');
    expect(screen.getByText(/no projects match/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /clear filters/i }));
    expect(names()).toHaveLength(3);
  });

  test('loading shows a busy status, not the empty state', () => {
    render(<DataTable label="Projects" columns={COLUMNS} rows={[]} getRowId={(r) => r.id} isLoading emptyState={<p>No projects yet</p>} />);
    expect(screen.getByRole('status')).toHaveTextContent(/loading/i);
    expect(screen.queryByText('No projects yet')).not.toBeInTheDocument();
  });

  test('selects rows, fills the bulk slot and clears', async () => {
    render(
      <DataTable
        label="Projects"
        columns={COLUMNS}
        rows={makeRows(3)}
        getRowId={(r) => r.id}
        selectable
        bulkActions={(selected) => <button type="button">Pause {selected.length}</button>}
      />,
    );
    expect(screen.queryByRole('button', { name: /pause/i })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select proj-01' }));
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select proj-02' }));
    expect(screen.getByRole('button', { name: 'Pause 2' })).toBeInTheDocument();
    expect(screen.getByText('2 selected')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('checkbox', { name: /select all rows on this page/i }));
    expect(screen.getByText('3 selected')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /clear selection/i }));
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  test('the selection is cleared when the page, the sort or the page size changes', async () => {
    render(
      <DataTable label="Projects" columns={COLUMNS} rows={makeRows(60)} getRowId={(r) => r.id} selectable
        bulkActions={(selected) => <button type="button">Pause {selected.length}</button>} />,
    );
    const pick = () => userEvent.click(screen.getAllByRole('checkbox', { name: /^Select proj-/ })[0]);
    await pick();
    expect(screen.getByText('1 selected')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /next page/i }));
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
    await pick();
    await userEvent.click(within(screen.getByRole('columnheader', { name: /size/i })).getByRole('button'));
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
    await pick();
    await userEvent.selectOptions(screen.getByLabelText(/rows per page/i), '50');
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  test('the column chooser hides a column and remembers it', async () => {
    const { unmount } = render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(2)} getRowId={(r) => r.id} storageKey="projects" />);
    await userEvent.click(screen.getByRole('button', { name: /columns/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: 'Plan' }));
    expect(screen.queryByRole('columnheader', { name: 'Plan' })).not.toBeInTheDocument();
    unmount();
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(2)} getRowId={(r) => r.id} storageKey="projects" />);
    expect(screen.queryByRole('columnheader', { name: 'Plan' })).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: /name/i })).toBeInTheDocument();
  });

  test('works when browser storage throws', async () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(2)} getRowId={(r) => r.id} storageKey="projects" />);
    await userEvent.click(screen.getByRole('button', { name: /columns/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: 'Size' }));
    expect(screen.queryByRole('columnheader', { name: 'Size' })).not.toBeInTheDocument();
    getItem.mockRestore();
    setItem.mockRestore();
  });

  test('server mode reports sort, search and filters and leaves the rows as given', async () => {
    const onSortChange = vi.fn();
    const onSearchChange = vi.fn();
    const onFilterChange = vi.fn();
    render(
      <DataTable label="Projects" columns={COLUMNS} rows={makeRows(3).reverse()} getRowId={(r) => r.id} filters={[PLAN_FILTER]}
        paging={{ mode: 'server', total: 90, page: 1, pageSize: 25, onPageChange: vi.fn(), onSortChange, onSearchChange, onFilterChange }} />,
    );
    await userEvent.click(within(screen.getByRole('columnheader', { name: /name/i })).getByRole('button'));
    expect(onSortChange).toHaveBeenLastCalledWith({ columnId: 'name', direction: 'asc' });
    await userEvent.type(screen.getByRole('searchbox', { name: /filter projects/i }), 'ab');
    expect(onSearchChange).toHaveBeenLastCalledWith('ab');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Plan' }), 'FREE');
    expect(onFilterChange).toHaveBeenLastCalledWith({ plan: 'FREE' });
    expect(names()).toEqual(['proj-03', 'proj-02', 'proj-01']);
  });

  test('row actions render in an Actions column for each row', async () => {
    const onOpen = vi.fn();
    render(<DataTable label="Projects" columns={COLUMNS} rows={makeRows(2)} getRowId={(r) => r.id}
      rowActions={(row) => <button type="button" onClick={() => onOpen(row.id)}>Open {row.name}</button>} />);
    expect(screen.getByRole('columnheader', { name: /actions/i })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Open proj-02' }));
    expect(onOpen).toHaveBeenCalledWith('r2');
  });
});
