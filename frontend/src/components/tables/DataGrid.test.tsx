import { describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { DataGrid } from './DataGrid';
import type { RowsResult } from '../../types/schema';

const ROWS: RowsResult = {
  columns: [
    { name: 'id', dataType: 'int4' },
    { name: 'title', dataType: 'text' },
    { name: 'meta', dataType: 'jsonb' },
  ],
  rows: [
    [1, 'first', { tag: 'a' }],
    [2, null, null],
  ],
  totalCount: 60,
};

function renderGrid(overrides: Partial<Parameters<typeof DataGrid>[0]> = {}) {
  const handlers = {
    onSortChange: vi.fn(),
    onPageChange: vi.fn(),
    onCellEdit: vi.fn(),
    onDeleteRow: vi.fn(),
  };
  render(
    <DataGrid
      rowsData={ROWS}
      rowsLoading={false}
      pkColumn="id"
      selectedTable="posts"
      sortCol="id"
      sortOrder="asc"
      page={0}
      pageSize={25}
      {...handlers}
      {...overrides}
    />,
  );
  return handlers;
}

function editCell(text: string) {
  fireEvent.doubleClick(screen.getByRole('button', { name: text }));
  return screen.getByDisplayValue(text === 'NULL' ? '' : text);
}

describe('DataGrid', () => {
  test('shows values, NULL and JSON cells', () => {
    renderGrid();
    expect(screen.getByText('first')).toBeInTheDocument();
    expect(screen.getAllByText('NULL')).toHaveLength(2);
    expect(screen.getByText('{"tag":"a"}')).toBeInTheDocument();
  });

  test('an edited cell is saved once on Enter, keyed by the primary key', () => {
    const { onCellEdit } = renderGrid();
    const editor = editCell('first');
    fireEvent.change(editor, { target: { value: 'renamed' } });
    fireEvent.keyDown(editor, { key: 'Enter' });
    fireEvent.blur(editor);

    expect(onCellEdit).toHaveBeenCalledTimes(1);
    expect(onCellEdit).toHaveBeenCalledWith('posts', 'id', '1', 'title', 'renamed');
    expect(screen.queryByDisplayValue('renamed')).not.toBeInTheDocument();
  });

  test('emptying a cell saves NULL', () => {
    const { onCellEdit } = renderGrid();
    const editor = editCell('first');
    fireEvent.change(editor, { target: { value: '' } });
    fireEvent.blur(editor);
    expect(onCellEdit).toHaveBeenCalledWith('posts', 'id', '1', 'title', null);
  });

  test('an unchanged cell is not saved', () => {
    const { onCellEdit } = renderGrid();
    fireEvent.blur(editCell('first'));
    expect(onCellEdit).not.toHaveBeenCalled();
  });

  test('Escape cancels an edit without saving', () => {
    const { onCellEdit } = renderGrid();
    const editor = editCell('first');
    fireEvent.change(editor, { target: { value: 'renamed' } });
    fireEvent.keyDown(editor, { key: 'Escape' });
    expect(onCellEdit).not.toHaveBeenCalled();
    expect(screen.getByText('first')).toBeInTheDocument();
  });

  test('a cell opens for editing from the keyboard', () => {
    renderGrid();
    fireEvent.keyDown(screen.getByRole('button', { name: 'first' }), { key: 'Enter' });
    expect(screen.getByDisplayValue('first')).toBeInTheDocument();
  });

  test('a table without a primary key cannot be edited or deleted from', () => {
    const { onCellEdit, onDeleteRow } = renderGrid({ pkColumn: '' });
    const editor = editCell('first');
    fireEvent.change(editor, { target: { value: 'renamed' } });
    fireEvent.blur(editor);
    expect(onCellEdit).not.toHaveBeenCalled();
    expect(onDeleteRow).not.toHaveBeenCalled();
  });

  test('delete passes the row primary key', () => {
    const { onDeleteRow } = renderGrid();
    const rows = screen.getAllByRole('row');
    const deleteButtons = rows[2].querySelectorAll('button');
    fireEvent.click(deleteButtons[deleteButtons.length - 1]);
    expect(onDeleteRow).toHaveBeenCalledWith('id', '2');
  });

  test('clicking a header sorts by it, again flips the order', () => {
    const { onSortChange } = renderGrid();
    fireEvent.click(screen.getByRole('button', { name: /^title/ }));
    expect(onSortChange).toHaveBeenLastCalledWith('title', 'asc');
    fireEvent.click(screen.getByRole('button', { name: /^id/ }));
    expect(onSortChange).toHaveBeenLastCalledWith('id', 'desc');
  });

  test('pages forward and back within the total', () => {
    const { onPageChange } = renderGrid({ page: 1 });
    expect(screen.getByText('Page 2 of 3')).toBeInTheDocument();
    const pagerButtons = screen.getByText('Page 2 of 3').parentElement!.querySelectorAll('button');
    fireEvent.click(pagerButtons[0]);
    expect(onPageChange).toHaveBeenLastCalledWith(0);
    fireEvent.click(pagerButtons[1]);
    expect(onPageChange).toHaveBeenLastCalledWith(2);
  });

  test('an empty table says so', () => {
    renderGrid({ rowsData: { columns: ROWS.columns, rows: [], totalCount: 0 } });
    expect(screen.getByText('No data')).toBeInTheDocument();
  });

  test('while loading it shows a skeleton, not the grid', () => {
    renderGrid({ rowsLoading: true });
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});
