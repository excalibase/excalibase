import type { AnyPermission, ColumnList, Operation, Presets } from '../../api/permissions';
import { isColumnName, isSessionVariable, validateBoolExpText } from '../../utils/permissionRules';

// The editor's form for one (table, role, operation) permission: plain text
// and lists while editing, the wire object on save.

export interface PresetRow {
  key: string;
  column: string;
  value: string;
}

export interface PermissionForm {
  filter: string;
  check: string;
  columns: ColumnList;
  limit: string;
  allowAggregations: boolean;
  presets: PresetRow[];
}

export interface FormErrors {
  filter?: string;
  check?: string;
  columns?: string;
  limit?: string;
  presets?: string;
}

const MAX_LIMIT = 2_147_483_647;

export const usesFilter = (op: Operation) => op !== 'insert';
export const usesCheck = (op: Operation) => op === 'insert' || op === 'update';
export const usesColumns = (op: Operation) => op !== 'delete';
export const usesPresets = (op: Operation) => op === 'insert' || op === 'update';

let presetKey = 0;
export function newPresetRow(column = '', value = ''): PresetRow {
  presetKey += 1;
  return { key: `preset-${presetKey}`, column, value };
}

const pretty = (value: unknown) => JSON.stringify(value, null, 2);

/** A new permission starts with nothing chosen, so nothing is granted by accident. */
export function initialForm(op: Operation, existing: AnyPermission | undefined): PermissionForm {
  const p = (existing ?? {}) as Partial<Record<string, unknown>>;
  const set = (p.set ?? {}) as Presets;
  let check = '';
  if (p.check) check = pretty(p.check);
  else if (existing && op === 'update') check = '{}';
  return {
    filter: p.filter ? pretty(p.filter) : '',
    check,
    columns: (p.columns as ColumnList | undefined) ?? [],
    limit: typeof p.limit === 'number' ? String(p.limit) : '',
    allowAggregations: p.allowAggregations === true,
    presets: Object.entries(set).map(([column, value]) => newPresetRow(column, String(value))),
  };
}

function expressionError(text: string, label: string): string | undefined {
  if (!text.trim()) return `Choose a ${label}: without any checks, owner only, or write an expression.`;
  return validateBoolExpText(text).error ?? undefined;
}

function limitError(text: string): string | undefined {
  if (!text.trim()) return undefined;
  const n = Number(text);
  if (!/^\d+$/.test(text.trim()) || n < 1 || n > MAX_LIMIT) {
    return 'Row limit must be a positive whole number, or empty for none.';
  }
  return undefined;
}

function presetsError(rows: PresetRow[]): string | undefined {
  if (rows.some((row) => !row.column)) return 'Pick a column for every preset.';
  const seen = new Set<string>();
  for (const row of rows) {
    if (seen.has(row.column)) return `"${row.column}" is preset twice.`;
    seen.add(row.column);
    if (!isColumnName(row.column)) return `"${row.column}" cannot be preset.`;
  }
  for (const row of rows) {
    if (row.value.toLowerCase().startsWith('x-excalibase-') && !isSessionVariable(row.value)) {
      return `"${row.value}" is not a valid session variable.`;
    }
  }
  return undefined;
}

function columnsError(op: Operation, form: PermissionForm): string | undefined {
  if (form.columns === '*' || form.columns.length > 0) return undefined;
  if (op === 'select') return 'Pick at least one column the role may read.';
  if (form.presets.length === 0) return 'Pick at least one column, or preset one.';
  return undefined;
}

export function formErrors(op: Operation, form: PermissionForm): FormErrors {
  const errors: FormErrors = {};
  if (usesFilter(op)) errors.filter = expressionError(form.filter, 'row filter');
  if (usesCheck(op)) errors.check = expressionError(form.check, 'row check');
  if (usesColumns(op)) errors.columns = columnsError(op, form);
  if (op === 'select') errors.limit = limitError(form.limit);
  if (usesPresets(op)) errors.presets = presetsError(form.presets);
  return errors;
}

export const hasErrors = (errors: FormErrors) => Object.values(errors).some(Boolean);

function presetsObject(rows: PresetRow[]): Presets | undefined {
  if (rows.length === 0) return undefined;
  return Object.fromEntries(rows.map((row) => [row.column, row.value]));
}

/** The wire object; call only when formErrors found nothing. */
export function buildPermission(op: Operation, form: PermissionForm): AnyPermission {
  const parse = (text: string) => JSON.parse(text) as Record<string, unknown>;
  switch (op) {
    case 'select': {
      const limit = form.limit.trim() ? { limit: Number(form.limit) } : {};
      return { filter: parse(form.filter), columns: form.columns, ...limit, allowAggregations: form.allowAggregations };
    }
    case 'insert': {
      const set = presetsObject(form.presets);
      return { check: parse(form.check), columns: form.columns, ...(set ? { set } : {}) };
    }
    case 'update': {
      const set = presetsObject(form.presets);
      return { filter: parse(form.filter), check: parse(form.check), columns: form.columns, ...(set ? { set } : {}) };
    }
    default:
      return { filter: parse(form.filter) };
  }
}
