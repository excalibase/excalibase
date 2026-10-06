import { api } from './client';
import { serverErrorMessage } from '../utils/serverError';

// Studio's table import (EXC-368). The server parses every file itself; this
// module only carries the file, the user's choices and the answers.

export type ImportColumnType =
  | 'text'
  | 'integer'
  | 'bigint'
  | 'numeric'
  | 'boolean'
  | 'date'
  | 'timestamptz'
  | 'uuid'
  | 'jsonb';

export const IMPORT_COLUMN_TYPES: ImportColumnType[] = [
  'text',
  'integer',
  'bigint',
  'numeric',
  'boolean',
  'date',
  'timestamptz',
  'uuid',
  'jsonb',
];

export interface PreviewColumn {
  source: number;
  sourceName: string;
  name: string;
  type: ImportColumnType;
}

export interface ImportLimits {
  maxBytes: number;
  maxXlsxBytes: number;
  maxRows: number;
  maxColumns: number;
}

export interface ImportPreview {
  format: 'csv' | 'xlsx';
  delimiter?: string;
  sheets?: string[];
  sheet?: string;
  hasHeader: boolean;
  columns: PreviewColumn[];
  rows: string[][];
  sampledRows: number;
  limits: ImportLimits;
}

export type ImportSource = { kind: 'file'; file: File } | { kind: 'sheets'; url: string };

export interface ReadSettings {
  hasHeader: boolean;
  delimiter?: string;
  sheet?: string;
}

export interface ImportOptions {
  schema: string;
  table: string;
  mode: 'create' | 'append';
  hasHeader: boolean;
  delimiter?: string;
  sheet?: string;
  columns: Array<{ source: number; name: string; type: ImportColumnType }>;
  primaryKey?: string;
}

export interface RowError {
  line: number;
  column?: string;
  value?: string;
  message: string;
}

export interface ImportResult {
  schema: string;
  table: string;
  mode: string;
  rows: number;
}

// An import can stream a large file into COPY; the server's own deadline is 15 minutes.
const IMPORT_TIMEOUT_MS = 16 * 60 * 1000;
const PREVIEW_TIMEOUT_MS = 2 * 60 * 1000;

export function buildPreviewForm(file: File, settings: ReadSettings): FormData {
  const form = new FormData();
  form.append('hasHeader', String(settings.hasHeader));
  if (settings.delimiter) form.append('delimiter', settings.delimiter);
  if (settings.sheet) form.append('sheet', settings.sheet);
  form.append('file', file);
  return form;
}

// The server reads the options before a byte of the file, so order matters.
export function buildImportForm(options: ImportOptions, file: File): FormData {
  const form = new FormData();
  form.append('options', JSON.stringify(options));
  form.append('file', file);
  return form;
}

const MULTIPART = { 'Content-Type': 'multipart/form-data' };

export async function previewImport(
  projectId: string,
  source: ImportSource,
  settings: ReadSettings,
): Promise<ImportPreview> {
  const url = `/schema/${projectId}/import/preview`;
  // The shared client defaults to JSON, which axios would turn a form into.
  const config = {
    timeout: PREVIEW_TIMEOUT_MS,
    ...(source.kind === 'file' && { headers: MULTIPART }),
  };
  const body =
    source.kind === 'file'
      ? buildPreviewForm(source.file, settings)
      : { sheetsUrl: source.url, ...settings };
  return (await api.post<ImportPreview>(url, body, config)).data;
}

export async function importTable(
  projectId: string,
  source: ImportSource,
  options: ImportOptions,
  onProgress?: (percent: number) => void,
): Promise<ImportResult> {
  const url = `/schema/${projectId}/import`;
  const config = {
    timeout: IMPORT_TIMEOUT_MS,
    ...(source.kind === 'file' && { headers: MULTIPART }),
    onUploadProgress: (event: { loaded: number; total?: number }) => {
      if (onProgress && event.total) onProgress(Math.round((event.loaded / event.total) * 100));
    },
  };
  const body =
    source.kind === 'file'
      ? buildImportForm(options, source.file)
      : { sheetsUrl: source.url, options };
  return (await api.post<ImportResult>(url, body, config)).data;
}

// A select permission with no row filter and every column: the table is
// readable by that role through the API (Hasura model, EXC-370).
export async function grantSelect(
  projectId: string,
  schema: string,
  table: string,
  role: 'anon' | 'user',
): Promise<void> {
  await api.put(
    `/provision/${projectId}/permissions/tables/${schema}.${table}/roles/${role}/select`,
    { filter: {}, columns: '*' },
  );
}

export interface ImportFailure {
  message: string;
  rowErrors: RowError[];
}

export function importErrorOf(err: unknown): ImportFailure {
  const data = (err as { response?: { data?: { error?: string; rowErrors?: RowError[] } } })
    .response?.data;
  if (data?.error) return { message: data.error, rowErrors: data.rowErrors ?? [] };
  return { message: serverErrorMessage(err, 'The import failed'), rowErrors: [] };
}

export interface ColumnChoice {
  include: boolean;
  name: string;
  type: ImportColumnType;
}

export interface TargetChoice {
  schema: string;
  table: string;
  mode: 'create' | 'append';
  primaryKey: string;
  columns: ColumnChoice[];
}

export function toImportOptions(preview: ImportPreview, target: TargetChoice): ImportOptions {
  const columns = preview.columns.flatMap((col, i) => {
    const choice = target.columns[i];
    return choice?.include ? [{ source: col.source, name: choice.name, type: choice.type }] : [];
  });
  return {
    schema: target.schema,
    table: target.table,
    mode: target.mode,
    hasHeader: preview.hasHeader,
    delimiter: preview.delimiter,
    sheet: preview.sheet,
    columns,
    primaryKey: target.mode === 'create' && target.primaryKey ? target.primaryKey : undefined,
  };
}

// Mirrors the server's rule so the form can say why before submitting.
export const IDENTIFIER_PATTERN = /^[a-z_][a-z0-9_]{0,62}$/;
