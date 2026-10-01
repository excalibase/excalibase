import type { AnyPermission, Operation, PermissionByOperation, PermissionDocument } from '../api/permissions';

// Reading a permission document for the Studio grid.

/** The roles every table grid shows: signed out and signed in. */
export const DEFAULT_ROLES = ['anon', 'user'] as const;

export type CellStatus = 'none' | 'full' | 'custom';

export function tableKey(schema: string, table: string): string {
  return `${schema || 'public'}.${table}`;
}

export function permissionFor<O extends Operation>(
  doc: PermissionDocument | undefined,
  table: string,
  role: string,
  operation: O,
): PermissionByOperation[O] | undefined {
  const entry = doc?.tables.find((t) => t.table === table && t.role === role);
  return entry?.[operation] as PermissionByOperation[O] | undefined;
}

function orderRoles(roles: Iterable<string>): string[] {
  const unique = [...new Set(roles)];
  const defaults = DEFAULT_ROLES.filter((role) => unique.includes(role));
  const others = unique.filter((role) => !(DEFAULT_ROLES as readonly string[]).includes(role)).sort((left, right) => left.localeCompare(right));
  return [...defaults, ...others];
}

/** anon and user, then every role the document names for the table, then added ones. */
export function gridRoles(doc: PermissionDocument | undefined, table: string, added: string[]): string[] {
  const fromDoc = doc?.tables.filter((t) => t.table === table).map((t) => t.role) ?? [];
  return orderRoles([...DEFAULT_ROLES, ...fromDoc, ...added]);
}

/** The roles that can read a table. */
export function rolesWithSelect(doc: PermissionDocument | undefined, table: string): string[] {
  const roles = doc?.tables.filter((t) => t.table === table && t.select).map((t) => t.role) ?? [];
  return orderRoles(roles);
}

const isEmpty = (value: object | undefined | null) => !value || Object.keys(value).length === 0;

/** none, full access (no row check, every column, no preset or limit) or custom. */
export function cellStatus(operation: Operation, permission: AnyPermission | undefined): CellStatus {
  if (!permission) return 'none';
  const p = permission as unknown as Partial<Record<string, unknown>>;
  const allColumns = operation === 'delete' || p.columns === '*';
  const full =
    allColumns &&
    isEmpty(p.filter as object) &&
    isEmpty(p.check as object) &&
    isEmpty(p.set as object) &&
    (p.limit === undefined || p.limit === null);
  return full ? 'full' : 'custom';
}

export function fullAccess<O extends Operation>(operation: O): PermissionByOperation[O] {
  const byOperation: PermissionByOperation = {
    select: { filter: {}, columns: '*' },
    insert: { check: {}, columns: '*' },
    update: { filter: {}, check: {}, columns: '*' },
    delete: { filter: {} },
  };
  return byOperation[operation];
}
