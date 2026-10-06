import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ImportTablePanel } from './ImportTablePanel';
import * as importApi from '../../api/tableImport';
import type { ImportPreview } from '../../api/tableImport';

vi.mock('../../api/tableImport', async (original) => {
  const actual = await original<typeof import('../../api/tableImport')>();
  return { ...actual, previewImport: vi.fn(), importTable: vi.fn(), grantSelect: vi.fn() };
});
vi.mock('../../hooks/useSchema', () => ({
  useTables: () => ({ data: [{ name: 'existing', schema: 'public', type: 'table' }] }),
}));

const preview: ImportPreview = {
  format: 'csv',
  delimiter: ',',
  hasHeader: true,
  sampledRows: 2,
  columns: [
    { source: 0, sourceName: 'Full Name', name: 'full_name', type: 'text' },
    { source: 1, sourceName: 'Age', name: 'age', type: 'integer' },
  ],
  rows: [
    ['ann', '31'],
    ['=cmd|calc', '40'],
  ],
  limits: { maxBytes: 50 << 20, maxXlsxBytes: 20 << 20, maxRows: 1_000_000, maxColumns: 500 },
};

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ImportTablePanel open onClose={vi.fn()} projectId="p1" />
    </QueryClientProvider>,
  );
}

async function uploadAndPreview() {
  const file = new File(['Full Name,Age\nann,31\n'], 'My People.csv', { type: 'text/csv' });
  await userEvent.upload(screen.getByTestId('import-file-input'), file);
  await userEvent.click(screen.getByTestId('import-preview-btn'));
  await screen.findByTestId('import-mapping');
  return file;
}

describe('ImportTablePanel', () => {
  beforeEach(() => {
    vi.mocked(importApi.previewImport).mockReset().mockResolvedValue(preview);
    vi.mocked(importApi.importTable).mockReset();
    vi.mocked(importApi.grantSelect).mockReset().mockResolvedValue();
  });

  test('previews a file and imports it with the edited columns and no permission by default', async () => {
    vi.mocked(importApi.importTable).mockResolvedValue({
      schema: 'public',
      table: 'my_people',
      mode: 'create',
      rows: 2,
    });
    renderPanel();
    const file = await uploadAndPreview();

    expect(screen.getByTestId('import-table-name')).toHaveValue('my_people');
    expect(screen.getByText('=cmd|calc')).toBeInTheDocument();
    await userEvent.clear(screen.getByTestId('import-col-name-0'));
    await userEvent.type(screen.getByTestId('import-col-name-0'), 'name');
    await userEvent.selectOptions(screen.getByTestId('import-col-type-1'), 'bigint');
    await userEvent.click(screen.getByTestId('import-submit'));

    await screen.findByTestId('import-result');
    expect(importApi.importTable).toHaveBeenCalledWith(
      'p1',
      { kind: 'file', file },
      expect.objectContaining({
        schema: 'public',
        table: 'my_people',
        mode: 'create',
        columns: [
          { source: 0, name: 'name', type: 'text' },
          { source: 1, name: 'age', type: 'bigint' },
        ],
      }),
      expect.any(Function),
    );
    expect(importApi.grantSelect).not.toHaveBeenCalled();
    expect(screen.getByTestId('import-result')).toHaveTextContent(/2 rows/);
    expect(screen.getByTestId('import-result')).toHaveTextContent(/no API role can read/i);
  });

  test('grants read only for the roles the user ticked', async () => {
    vi.mocked(importApi.importTable).mockResolvedValue({
      schema: 'public',
      table: 'my_people',
      mode: 'create',
      rows: 2,
    });
    renderPanel();
    await uploadAndPreview();
    await userEvent.click(screen.getByTestId('import-grant-user'));
    await userEvent.click(screen.getByTestId('import-submit'));
    await waitFor(() =>
      expect(importApi.grantSelect).toHaveBeenCalledWith('p1', 'public', 'my_people', 'user'),
    );
    expect(importApi.grantSelect).toHaveBeenCalledTimes(1);
  });

  test('shows row errors with their line numbers', async () => {
    vi.mocked(importApi.importTable).mockRejectedValue({
      response: {
        status: 422,
        data: {
          error: '1 row(s) could not be imported',
          rowErrors: [
            {
              line: 7,
              column: 'age',
              value: 'x',
              message: 'integer is not a whole number in range',
            },
          ],
        },
      },
    });
    renderPanel();
    await uploadAndPreview();
    await userEvent.click(screen.getByTestId('import-submit'));
    const errors = await screen.findByTestId('import-row-errors');
    expect(errors).toHaveTextContent('7');
    expect(errors).toHaveTextContent('age');
  });

  test('an invalid table name blocks the import', async () => {
    renderPanel();
    await uploadAndPreview();
    await userEvent.clear(screen.getByTestId('import-table-name'));
    await userEvent.type(screen.getByTestId('import-table-name'), 'Bad Name');
    expect(screen.getByTestId('import-submit')).toBeDisabled();
  });

  test('a Google Sheets link is previewed by URL', async () => {
    renderPanel();
    await userEvent.click(screen.getByTestId('import-source-sheets'));
    await userEvent.type(
      screen.getByTestId('import-sheets-url'),
      'https://docs.google.com/spreadsheets/d/abc/edit',
    );
    await userEvent.click(screen.getByTestId('import-preview-btn'));
    await screen.findByTestId('import-mapping');
    expect(importApi.previewImport).toHaveBeenCalledWith(
      'p1',
      { kind: 'sheets', url: 'https://docs.google.com/spreadsheets/d/abc/edit' },
      { hasHeader: true },
    );
  });

  test('a refused preview shows the server message', async () => {
    vi.mocked(importApi.previewImport).mockRejectedValue({
      response: { status: 415, data: { error: 'the file is not a CSV, TSV or XLSX file' } },
    });
    renderPanel();
    await userEvent.upload(
      screen.getByTestId('import-file-input'),
      new File(['\x7fELF'], 'renamed.csv'),
    );
    await userEvent.click(screen.getByTestId('import-preview-btn'));
    expect(await screen.findByTestId('import-error')).toHaveTextContent('not a CSV');
  });

  test('changing how the file is read previews it again', async () => {
    renderPanel();
    await uploadAndPreview();
    await userEvent.selectOptions(screen.getByTestId('import-delimiter'), ';');
    await waitFor(() =>
      expect(importApi.previewImport).toHaveBeenLastCalledWith(
        'p1',
        expect.objectContaining({ kind: 'file' }),
        { hasHeader: true, delimiter: ';' },
      ),
    );
    await userEvent.click(screen.getByTestId('import-has-header'));
    await waitFor(() =>
      expect(importApi.previewImport).toHaveBeenLastCalledWith(
        'p1',
        expect.anything(),
        expect.objectContaining({ hasHeader: false }),
      ),
    );
  });

  test('a workbook offers its sheets', async () => {
    vi.mocked(importApi.previewImport).mockResolvedValue({
      ...preview,
      format: 'xlsx',
      delimiter: undefined,
      sheets: ['Stock', 'Other'],
      sheet: 'Stock',
    });
    renderPanel();
    await uploadAndPreview();
    expect(screen.queryByTestId('import-delimiter')).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByTestId('import-sheet'), 'Other');
    await waitFor(() =>
      expect(importApi.previewImport).toHaveBeenLastCalledWith(
        'p1',
        expect.anything(),
        expect.objectContaining({ sheet: 'Other' }),
      ),
    );
  });

  test('append skips a column, names the table and grants nothing', async () => {
    vi.mocked(importApi.importTable).mockResolvedValue({
      schema: 'public',
      table: 'existing',
      mode: 'append',
      rows: 2,
    });
    renderPanel();
    await uploadAndPreview();
    await userEvent.selectOptions(screen.getByTestId('import-mode'), 'append');
    expect(screen.queryByTestId('import-grant-anon')).not.toBeInTheDocument();
    expect(screen.queryByTestId('import-primary-key')).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByTestId('import-table-name'), 'existing');
    await userEvent.click(screen.getByTestId('import-col-include-1'));
    await userEvent.click(screen.getByTestId('import-submit'));
    await screen.findByTestId('import-result');
    expect(importApi.importTable).toHaveBeenCalledWith(
      'p1',
      expect.anything(),
      expect.objectContaining({
        mode: 'append',
        table: 'existing',
        columns: [{ source: 0, name: 'full_name', type: 'text' }],
      }),
      expect.any(Function),
    );
    expect(importApi.grantSelect).not.toHaveBeenCalled();
  });

  test('a file with an id column has it preselected as the primary key', async () => {
    vi.mocked(importApi.previewImport).mockResolvedValue({
      ...preview,
      columns: [
        { source: 0, sourceName: 'id', name: 'id', type: 'integer' },
        { source: 1, sourceName: 'Age', name: 'age', type: 'integer' },
      ],
    });
    vi.mocked(importApi.importTable).mockResolvedValue({ schema: 'public', table: 'my_people', mode: 'create', rows: 2 });
    renderPanel();
    await uploadAndPreview();
    expect(screen.getByTestId('import-primary-key')).toHaveValue('id');
    await userEvent.click(screen.getByTestId('import-submit'));
    await screen.findByTestId('import-result');
    expect(importApi.importTable).toHaveBeenCalledWith(
      'p1',
      expect.anything(),
      expect.objectContaining({ primaryKey: 'id' }),
      expect.any(Function),
    );
  });

  test('a chosen primary key and schema are sent', async () => {
    vi.mocked(importApi.importTable).mockResolvedValue({
      schema: 'sales',
      table: 'my_people',
      mode: 'create',
      rows: 2,
    });
    renderPanel();
    await uploadAndPreview();
    await userEvent.clear(screen.getByTestId('import-schema'));
    await userEvent.type(screen.getByTestId('import-schema'), 'sales');
    await userEvent.selectOptions(screen.getByTestId('import-primary-key'), 'full_name');
    await userEvent.click(screen.getByTestId('import-grant-anon'));
    await userEvent.click(screen.getByTestId('import-submit'));
    await screen.findByTestId('import-result');
    expect(importApi.importTable).toHaveBeenCalledWith(
      'p1',
      expect.anything(),
      expect.objectContaining({ schema: 'sales', primaryKey: 'full_name' }),
      expect.any(Function),
    );
    expect(importApi.grantSelect).toHaveBeenCalledWith('p1', 'sales', 'my_people', 'anon');
    expect(screen.getByTestId('import-result')).toHaveTextContent(/anon/);
    await userEvent.click(screen.getByTestId('import-done'));
  });
});
