import { describe, test, expect } from 'vitest';
import type { PermissionDocument } from '../api/permissions';
import { cellStatus, fullAccess, gridRoles, permissionFor, rolesWithSelect, tableKey } from './permissionModel';

const DOC: PermissionDocument = {
  projectId: 'p1',
  version: 1,
  tables: [
    { table: 'public.orders', role: 'user', select: { filter: {}, columns: '*' }, insert: { check: {}, columns: ['total'] } },
    { table: 'public.orders', role: 'editor', update: { filter: {}, columns: '*' } },
    { table: 'public.orders', role: 'anon', select: { filter: { a: { _eq: 1 } }, columns: '*' } },
    { table: 'public.customers', role: 'manager', select: { filter: {}, columns: '*' } },
  ],
  functions: [],
  functionPermissions: [],
};

describe('permission model', () => {
  test('tableKey qualifies a table', () => {
    expect(tableKey('public', 'orders')).toBe('public.orders');
    expect(tableKey('', 'orders')).toBe('public.orders');
  });

  test('permissionFor finds one operation', () => {
    expect(permissionFor(DOC, 'public.orders', 'user', 'insert')).toEqual({ check: {}, columns: ['total'] });
    expect(permissionFor(DOC, 'public.orders', 'user', 'delete')).toBeUndefined();
    expect(permissionFor(undefined, 'public.orders', 'user', 'select')).toBeUndefined();
  });

  test('grid roles: anon and user always first, then the table roles, then added ones', () => {
    expect(gridRoles(DOC, 'public.orders', ['zeta', 'editor'])).toEqual(['anon', 'user', 'editor', 'zeta']);
    expect(gridRoles(undefined, 'public.x', [])).toEqual(['anon', 'user']);
  });

  test('rolesWithSelect lists who can read a table', () => {
    expect(rolesWithSelect(DOC, 'public.orders')).toEqual(['anon', 'user']);
    expect(rolesWithSelect(DOC, 'public.customers')).toEqual(['manager']);
    expect(rolesWithSelect(DOC, 'public.none')).toEqual([]);
  });

  test.each([
    ['select', undefined, 'none'],
    ['select', { filter: {}, columns: '*' }, 'full'],
    ['select', { filter: {}, columns: '*', limit: null, allowAggregations: true }, 'full'],
    ['select', { filter: {}, columns: '*', limit: 10 }, 'custom'],
    ['select', { filter: { a: { _eq: 1 } }, columns: '*' }, 'custom'],
    ['select', { filter: {}, columns: ['id'] }, 'custom'],
    ['insert', { check: {}, columns: '*' }, 'full'],
    ['insert', { check: {}, columns: '*', set: { owner: 'X-Excalibase-User-Id' } }, 'custom'],
    ['insert', { check: {}, columns: '*', set: {} }, 'full'],
    ['update', { filter: {}, columns: '*' }, 'full'],
    ['update', { filter: {}, check: {}, columns: '*', set: {} }, 'full'],
    ['update', { filter: {}, check: { a: { _eq: 1 } }, columns: '*' }, 'custom'],
    ['delete', { filter: {} }, 'full'],
    ['delete', { filter: { a: { _eq: 1 } } }, 'custom'],
  ] as const)('%s %j is %s', (op, permission, status) => {
    expect(cellStatus(op, permission as never)).toBe(status);
  });

  test('fullAccess builds each operation with no checks and every column', () => {
    expect(fullAccess('select')).toEqual({ filter: {}, columns: '*' });
    expect(fullAccess('insert')).toEqual({ check: {}, columns: '*' });
    expect(fullAccess('update')).toEqual({ filter: {}, check: {}, columns: '*' });
    expect(fullAccess('delete')).toEqual({ filter: {} });
  });
});
