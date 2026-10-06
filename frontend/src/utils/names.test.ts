import { describe, expect, test } from 'vitest';
import { identifierBytes, identifierError, USERNAME_RULE, usernameError } from './names';

describe('usernameError', () => {
  test('accepts 3-32 letters, numbers or underscores', () => {
    for (const name of ['bob', 'Alice_2', 'a'.repeat(32)]) expect(usernameError(name)).toBeUndefined();
  });

  test('refuses anything else with the one rule', () => {
    for (const name of ['jo', 'a'.repeat(33), 'dash-name', 'dot.name', 'bad name', 'émile']) {
      expect(usernameError(name)).toBe(USERNAME_RULE);
    }
    expect(USERNAME_RULE).toBe('Use 3–32 letters, numbers or underscores');
  });
});

describe('identifierError', () => {
  test('63 bytes is the limit Postgres keeps', () => {
    expect(identifierError('Table', 'a'.repeat(63))).toBeUndefined();
    expect(identifierError('Table', 'a'.repeat(64))).toBe('Table names can be at most 63 characters; this one has 64');
    expect(identifierError('Column', 'a'.repeat(70))).toBe('Column names can be at most 63 characters; this one has 70');
  });

  test('counts bytes the way Postgres does', () => {
    expect(identifierBytes('é')).toBe(2);
    expect(identifierError('Table', 'é'.repeat(32))).toBe('Table names can be at most 63 characters; this one has 64');
  });
});
