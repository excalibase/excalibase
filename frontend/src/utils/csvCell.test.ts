import { describe, it, expect } from 'vitest';
import { csvCell } from './csvCell';

describe('csvCell', () => {
  it('quotes a value and doubles its quotes', () => {
    expect(csvCell('a "b"')).toBe('"a ""b"""');
  });

  it('defuses cells a spreadsheet would run as a formula', () => {
    for (const formula of ["=cmd|' /C calc'!A0", '+1+1', '-2+3', '@SUM(A1)', '\tx', '\rx']) {
      expect(csvCell(formula).startsWith(`"'`)).toBe(true);
    }
  });

  it('leaves plain numbers alone, negative ones included', () => {
    expect(csvCell('-42')).toBe('"-42"');
    expect(csvCell('3.5')).toBe('"3.5"');
  });

  it('writes null as an empty cell and objects as JSON', () => {
    expect(csvCell(null)).toBe('');
    expect(csvCell({ a: 1 })).toBe('"{""a"":1}"');
  });
});

describe('csvCell values', () => {
  it('writes numbers, booleans and big integers as text', () => {
    expect(csvCell(7)).toBe('"7"');
    expect(csvCell(false)).toBe('"false"');
    expect(csvCell(BigInt(9))).toBe('"9"');
  });

  it('writes an unserialisable value as an empty cell', () => {
    const loop: Record<string, unknown> = {};
    loop.self = loop;
    expect(csvCell(loop)).toBe('""');
  });
});
