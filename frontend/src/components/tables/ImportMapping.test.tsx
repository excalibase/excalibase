import { describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ImportMapping } from './ImportMapping';
import type { ImportPreview, TargetChoice } from '../../api/tableImport';

const PREVIEW: ImportPreview = {
  format: 'csv',
  delimiter: ',',
  hasHeader: true,
  columns: [
    { source: 0, sourceName: 'Full Name', name: 'full_name', type: 'text' },
    { source: 1, sourceName: 'Age', name: 'age', type: 'integer' },
  ],
  rows: Array.from({ length: 12 }, (_, line) => [`person ${line}`, String(20 + line)]),
  sampledRows: 12,
  limits: { maxBytes: 1, maxXlsxBytes: 1, maxRows: 1, maxColumns: 1 },
};

const TARGET: TargetChoice = {
  schema: 'public',
  table: 'people',
  mode: 'create',
  primaryKey: '',
  columns: [
    { include: true, name: 'full_name', type: 'text' },
    { include: true, name: 'age', type: 'integer' },
  ],
};

function renderMapping(target: TargetChoice = TARGET, preview: ImportPreview = PREVIEW) {
  const onTargetChange = vi.fn();
  const onReadChange = vi.fn();
  render(
    <ImportMapping
      preview={preview}
      target={target}
      existingTables={['customers', 'orders']}
      onTargetChange={onTargetChange}
      onReadChange={onReadChange}
    />,
  );
  return { onTargetChange, onReadChange };
}

const NAME_HINT = /Use lower-case letters, digits and underscores/;

describe('ImportMapping', () => {
  test('an empty table name shows no format error yet', () => {
    renderMapping({ ...TARGET, table: '' });
    expect(screen.queryByText(NAME_HINT)).not.toBeInTheDocument();
  });

  test('a table name in the wrong format is flagged', () => {
    renderMapping({ ...TARGET, table: 'My Table' });
    expect(screen.getByText(NAME_HINT)).toBeInTheDocument();
  });

  test('a table name longer than 63 characters is flagged', () => {
    renderMapping({ ...TARGET, table: `t${'a'.repeat(63)}` });
    expect(screen.getByText(NAME_HINT)).toBeInTheDocument();
  });

  test('a valid table name is not flagged', () => {
    renderMapping();
    expect(screen.queryByText(NAME_HINT)).not.toBeInTheDocument();
  });

  test('an invalid column name is outlined', () => {
    renderMapping({ ...TARGET, columns: [{ include: true, name: 'Full Name', type: 'text' }, TARGET.columns[1]] });
    expect(screen.getByTestId('import-col-name-0').className).toContain('border-red-500');
    expect(screen.getByTestId('import-col-name-1').className).not.toContain('border-red-500');
  });

  test('editing the target passes the whole choice up', () => {
    const { onTargetChange } = renderMapping();
    fireEvent.change(screen.getByTestId('import-table-name'), { target: { value: 'members' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({ ...TARGET, table: 'members' });

    fireEvent.change(screen.getByTestId('import-schema'), { target: { value: 'crm' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({ ...TARGET, schema: 'crm' });

    fireEvent.change(screen.getByTestId('import-mode'), { target: { value: 'append' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({ ...TARGET, mode: 'append' });

    fireEvent.change(screen.getByTestId('import-primary-key'), { target: { value: 'age' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({ ...TARGET, primaryKey: 'age' });
  });

  test('each column can be skipped, renamed and retyped', () => {
    const { onTargetChange } = renderMapping();
    fireEvent.click(screen.getByLabelText('Import Age'));
    expect(onTargetChange).toHaveBeenLastCalledWith({
      ...TARGET,
      columns: [TARGET.columns[0], { ...TARGET.columns[1], include: false }],
    });

    fireEvent.change(screen.getByTestId('import-col-name-0'), { target: { value: 'name' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({
      ...TARGET,
      columns: [{ ...TARGET.columns[0], name: 'name' }, TARGET.columns[1]],
    });

    fireEvent.change(screen.getByTestId('import-col-type-1'), { target: { value: 'text' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({
      ...TARGET,
      columns: [TARGET.columns[0], { ...TARGET.columns[1], type: 'text' }],
    });
  });

  test('append mode picks an existing table and has no primary key choice', () => {
    const { onTargetChange } = renderMapping({ ...TARGET, mode: 'append', table: '' });
    expect(screen.queryByTestId('import-primary-key')).not.toBeInTheDocument();
    fireEvent.change(screen.getByTestId('import-table-name'), { target: { value: 'orders' } });
    expect(onTargetChange).toHaveBeenLastCalledWith({ ...TARGET, mode: 'append', table: 'orders' });
  });

  test('read settings for CSV change header and separator', () => {
    const { onReadChange } = renderMapping();
    fireEvent.click(screen.getByTestId('import-has-header'));
    expect(onReadChange).toHaveBeenLastCalledWith({ hasHeader: false });
    fireEvent.change(screen.getByTestId('import-delimiter'), { target: { value: ';' } });
    expect(onReadChange).toHaveBeenLastCalledWith({ delimiter: ';' });
    expect(screen.queryByTestId('import-sheet')).not.toBeInTheDocument();
  });

  test('a workbook with several sheets offers a sheet choice', () => {
    const { onReadChange } = renderMapping(TARGET, {
      ...PREVIEW,
      format: 'xlsx',
      delimiter: undefined,
      sheets: ['Jan', 'Feb'],
      sheet: 'Jan',
    });
    expect(screen.queryByTestId('import-delimiter')).not.toBeInTheDocument();
    fireEvent.change(screen.getByTestId('import-sheet'), { target: { value: 'Feb' } });
    expect(onReadChange).toHaveBeenLastCalledWith({ sheet: 'Feb' });
  });

  test('previews at most ten rows, as text', () => {
    renderMapping(TARGET, { ...PREVIEW, rows: [['=HYPERLINK("x")', '1'], ...PREVIEW.rows] });
    expect(screen.getByText('First rows (10 of 12 read)')).toBeInTheDocument();
    expect(screen.getByText('=HYPERLINK("x")')).toBeInTheDocument();
    expect(screen.getAllByRole('row')).toHaveLength(11);
  });
});
