import { describe, test, expect } from 'vitest';
import { pageRange, sortRows, errorKind } from './model';

describe('data table model', () => {
  test('pageRange is 1-based and capped at the total', () => {
    expect(pageRange(1, 25, 90)).toEqual({ from: 1, to: 25 });
    expect(pageRange(4, 25, 90)).toEqual({ from: 76, to: 90 });
    expect(pageRange(1, 25, 0)).toEqual({ from: 0, to: 0 });
  });

  test('sortRows puts missing values last and keeps the input intact', () => {
    const rows = [{ v: 2 }, { v: null }, { v: 1 }];
    const sorted = sortRows(rows, (r) => r.v, 'asc');
    expect(sorted.map((r) => r.v)).toEqual([1, 2, null]);
    expect(sortRows(rows, (r) => r.v, 'desc').map((r) => r.v)).toEqual([2, 1, null]);
    expect(rows.map((r) => r.v)).toEqual([2, null, 1]);
  });

  test('errorKind tells a 403 from other failures', () => {
    expect(errorKind({ response: { status: 403 } })).toBe('forbidden');
    expect(errorKind({ response: { status: 500 } })).toBe('failed');
    expect(errorKind(new Error('x'))).toBe('failed');
  });
});
