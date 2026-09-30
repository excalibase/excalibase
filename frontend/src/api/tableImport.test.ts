import { describe, it, expect, vi, beforeEach } from 'vitest';
import { api } from './client';
import {
  buildImportForm,
  buildPreviewForm,
  importTable,
  previewImport,
  grantSelect,
  importErrorOf,
  toImportOptions,
  type ImportPreview,
} from './tableImport';

vi.mock('./client', () => ({ api: { post: vi.fn(), put: vi.fn() } }));

const options = {
  schema: 'public',
  table: 'people',
  mode: 'create' as const,
  hasHeader: true,
  columns: [{ source: 0, name: 'name', type: 'text' as const }],
};

describe('tableImport api', () => {
  beforeEach(() => vi.clearAllMocks());

  it('sends the options before the file, as the server requires', () => {
    const file = new File(['a\n1\n'], 'a.csv');
    const keys = [...buildImportForm(options, file).keys()];
    expect(keys).toEqual(['options', 'file']);
  });

  it('sends preview settings before the file', () => {
    const file = new File(['a\n1\n'], 'a.csv');
    const form = buildPreviewForm(file, { hasHeader: false, delimiter: ';', sheet: 'Data' });
    expect([...form.keys()]).toEqual(['hasHeader', 'delimiter', 'sheet', 'file']);
    expect(form.get('hasHeader')).toBe('false');
  });

  it('posts a Google Sheets link as JSON and never a file', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { columns: [], rows: [] } });
    await previewImport(
      'p1',
      { kind: 'sheets', url: 'https://docs.google.com/spreadsheets/d/x' },
      { hasHeader: true },
    );
    expect(api.post).toHaveBeenCalledWith(
      '/schema/p1/import/preview',
      { sheetsUrl: 'https://docs.google.com/spreadsheets/d/x', hasHeader: true },
      expect.any(Object),
    );
  });

  it('gives a long import its own timeout and reports upload progress', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { rows: 3 } });
    const progress = vi.fn();
    const file = new File(['a\n1\n'], 'a.csv');
    await importTable('p1', { kind: 'file', file }, options, progress);
    const config = vi.mocked(api.post).mock.calls[0][2] as {
      timeout: number;
      onUploadProgress: (e: { loaded: number; total?: number }) => void;
    };
    expect(config.timeout).toBeGreaterThanOrEqual(15 * 60 * 1000);
    config.onUploadProgress({ loaded: 50, total: 100 });
    expect(progress).toHaveBeenCalledWith(50);
  });

  it('grants a read-all select permission for a role', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} });
    await grantSelect('p1', 'public', 'people', 'anon');
    expect(api.put).toHaveBeenCalledWith(
      '/provision/p1/permissions/tables/public.people/roles/anon/select',
      { filter: {}, columns: '*' },
    );
  });

  it('reads row errors out of a refusal', () => {
    const err = {
      response: {
        status: 422,
        data: { error: 'bad rows', rowErrors: [{ line: 3, message: 'x' }] },
      },
    };
    expect(importErrorOf(err)).toEqual({
      message: 'bad rows',
      rowErrors: [{ line: 3, message: 'x' }],
    });
    expect(importErrorOf(new Error('network'))).toEqual({ message: 'network', rowErrors: [] });
  });

  it('turns an edited preview into options, leaving skipped columns out', () => {
    const preview: ImportPreview = {
      format: 'csv',
      hasHeader: true,
      rows: [],
      sampledRows: 0,
      columns: [
        { source: 0, sourceName: 'Name', name: 'name', type: 'text' },
        { source: 1, sourceName: 'Age', name: 'age', type: 'integer' },
      ],
      limits: { maxBytes: 1, maxXlsxBytes: 1, maxRows: 1, maxColumns: 1 },
    };
    const built = toImportOptions(preview, {
      schema: 'public',
      table: 'people',
      mode: 'create',
      primaryKey: '',
      columns: [
        { include: true, name: 'full_name', type: 'text' },
        { include: false, name: 'age', type: 'integer' },
      ],
    });
    expect(built.columns).toEqual([{ source: 0, name: 'full_name', type: 'text' }]);
    expect(built.hasHeader).toBe(true);
    expect(built.primaryKey).toBeUndefined();
  });
});
