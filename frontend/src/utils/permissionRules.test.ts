import { describe, test, expect } from 'vitest';
import {
  validateBoolExp,
  validateBoolExpText,
  validatePermissionRole,
  validateEndUserRole,
  isSessionVariable,
  ownerOnlyExpression,
  jsonArguments,
  ROLE_PATTERN,
} from './permissionRules';

describe('validateBoolExp', () => {
  test.each([
    ['empty object means every row', {}],
    ['comparison with a session variable', { owner_id: { _eq: 'X-Excalibase-User-Id' } }],
    ['several operators on one column', { total: { _gt: 1, _lte: 100 } }],
    ['_and/_or/_not', { _and: [{ a: { _eq: 1 } }, { _or: [{ b: { _neq: 'x' } }, { _not: { c: { _is_null: true } } }] }] }],
    ['_in with a list', { status: { _in: ['open', 'paid'] } }],
    ['_nin with a session variable holding an array', { team: { _nin: 'x-excalibase-teams' } }],
    ['_like pattern', { email: { _like: '%@x.test' } }],
    ['relationship', { customer: { owner_id: { _eq: 'X-Excalibase-User-Id' } } }],
    ['_exists', { _exists: { _table: 'public.members', _where: { user_id: { _eq: 'X-Excalibase-User-Id' } } } }],
    ['boolean and number literals', { active: { _eq: true }, n: { _eq: 3 } }],
    ['claim with underscore', { tenant: { _eq: 'X-Excalibase-tenant_id' } }],
  ])('accepts %s', (_name, exp) => {
    expect(validateBoolExp(exp)).toBeNull();
  });

  test.each([
    ['an array', [], /must be a JSON object/],
    ['null', null, /must be a JSON object/],
    ['a bad column name', { 'bad name': { _eq: 1 } }, /not a column or relationship name/],
    ['an unknown operator next to a known one', { a: { _eq: 1, _foo: 2 } }, /"_foo" is not a comparison operator/],
    ['null operand', { a: { _eq: null } }, /null is not comparable; use _is_null/],
    ['object operand', { a: { _eq: { x: 1 } } }, /takes a string, number, boolean or session variable/],
    ['_is_null with a string', { a: { _is_null: 'yes' } }, /takes true or false/],
    ['_in with a scalar', { a: { _in: 'open' } }, /takes an array or a session variable holding one/],
    ['_in element that is a session variable', { a: { _in: ['X-Excalibase-User-Id'] } }, /array element cannot be a session variable/],
    ['_in element that is an object', { a: { _in: [{}] } }, /array elements must be strings, numbers or booleans/],
    ['_like with a number', { a: { _like: 3 } }, /takes a string pattern or a session variable/],
    ['malformed session variable', { a: { _eq: 'X-Excalibase-' } }, /is not a valid session variable/],
    ['_and with an object', { _and: { a: { _eq: 1 } } }, /_and takes an array of expressions/],
    ['_exists without _where', { _exists: { _table: 'public.t' } }, /_exists takes exactly/],
    ['_exists with a bad table', { _exists: { _table: 'Members', _where: {} } }, /_exists._table/],
    ['_exists with a non-string table', { _exists: { _table: 3, _where: {} } }, /_exists._table must be a string/],
    ['a column mapped to a scalar', { a: 1 }, /"a" must map to an object/],
  ])('refuses %s', (_name, exp, message) => {
    expect(validateBoolExp(exp)).toMatch(message);
  });

  test('refuses nesting deeper than 16', () => {
    let exp: unknown = {};
    for (let i = 0; i < 17; i++) exp = { _not: exp };
    expect(validateBoolExp(exp)).toMatch(/nested deeper than 16/);
  });

  test('refuses more than 200 nodes', () => {
    const items = Array.from({ length: 201 }, () => ({}));
    expect(validateBoolExp({ _and: items })).toMatch(/more than 200 nodes/);
  });
});

describe('validateBoolExpText', () => {
  test('parses and validates', () => {
    expect(validateBoolExpText('{"a": {"_eq": 1}}')).toEqual({ value: { a: { _eq: 1 } }, error: null });
  });

  test('reports invalid JSON', () => {
    expect(validateBoolExpText('{a:').error).toMatch(/not valid JSON/);
  });

  test('reports grammar errors', () => {
    expect(validateBoolExpText('[]').error).toMatch(/must be a JSON object/);
  });
});

describe('roles', () => {
  test('ROLE_PATTERN matches the server', () => {
    expect(ROLE_PATTERN.test('editor_2')).toBe(true);
    expect(ROLE_PATTERN.test('2editor')).toBe(false);
    expect(ROLE_PATTERN.test('a'.repeat(64))).toBe(false);
  });

  test('a permission role cannot be service or malformed', () => {
    expect(validatePermissionRole('editor')).toBeNull();
    expect(validatePermissionRole('service')).toMatch(/bypasses permissions/);
    expect(validatePermissionRole('Editor')).toMatch(/lower-case letters/);
  });

  test('an end-user role refuses the reserved names', () => {
    expect(validateEndUserRole('editor')).toBeNull();
    for (const reserved of ['anon', 'service', 'pg_read', 'excalibase_x']) {
      expect(validateEndUserRole(reserved)).toMatch(/reserved/);
    }
    expect(validateEndUserRole('Bad')).toMatch(/lower-case letters/);
  });
});

describe('helpers', () => {
  test('isSessionVariable is case-insensitive', () => {
    expect(isSessionVariable('x-excalibase-user-id')).toBe(true);
    expect(isSessionVariable('X-Hasura-User-Id')).toBe(false);
  });

  test('ownerOnlyExpression compares a column to the user id', () => {
    expect(ownerOnlyExpression('owner_id')).toEqual({ owner_id: { _eq: 'X-Excalibase-User-Id' } });
  });

  test('jsonArguments picks json and jsonb arguments from an identity argument list', () => {
    expect(jsonArguments('p_query text, session jsonb, extra json, n integer')).toEqual(['session', 'extra']);
    expect(jsonArguments('')).toEqual([]);
    expect(jsonArguments('jsonb')).toEqual([]);
    expect(jsonArguments('IN claims jsonb')).toEqual(['claims']);
  });
});
