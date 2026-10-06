import { describe, expect, test } from 'vitest';
import { identifierBytes, identifierError, NAME_RULE, newNameError, USERNAME_RULE, usernameError } from './names';

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

describe('newNameError', () => {
  test('accepts the names the API can expose, ignoring surrounding spaces', () => {
    for (const name of ['orders', '_tmp', 'order_items_2', ' notes ', 'a'.repeat(63)]) {
      expect(newNameError('Table', name)).toBeUndefined();
    }
  });

  test('a blank name has no message; the submit is simply off', () => {
    expect(newNameError('Table', '   ')).toBeUndefined();
  });

  test('refuses capitals, spaces, dashes, a leading digit and accents with the server rule', () => {
    for (const name of ['Orders', 'my table', 'order-items', '2nd', 'café']) {
      expect(newNameError('Column', name)).toBe(NAME_RULE);
    }
    expect(NAME_RULE).toBe(
      'Use lowercase letters, numbers and underscores, starting with a letter or underscore; at most 63 characters.',
    );
  });

  test('an over-long name keeps the length message with its count', () => {
    expect(newNameError('Table', 'a'.repeat(64))).toBe('Table names can be at most 63 characters; this one has 64');
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
