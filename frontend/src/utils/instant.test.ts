import { afterEach, describe, expect, test, vi } from 'vitest';
import { parseInstant } from './instant';

afterEach(() => vi.restoreAllMocks());

describe('parseInstant', () => {
  test('reads Z and offset forms as the same instant', () => {
    const expected = Date.UTC(2026, 10, 25, 2, 30, 0);
    expect(parseInstant('2026-11-25T02:30:00Z')).toBe(expected);
    expect(parseInstant('2026-11-25T09:30:00+07:00')).toBe(expected);
    expect(parseInstant('2026-11-25T02:30:00.123456789Z')).toBe(expected + 123);
  });

  test('refuses a zoneless time instead of guessing its zone', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    expect(Number.isNaN(parseInstant('2026-11-25T02:30:00'))).toBe(true);
    expect(Number.isNaN(parseInstant('2026-11-25T02:30:00.000000000'))).toBe(true);
    expect(warn).toHaveBeenCalled();
  });

  test('accepts epoch millis and rejects empty or unreadable values', () => {
    expect(parseInstant(1_700_000_000_000)).toBe(1_700_000_000_000);
    expect(Number.isNaN(parseInstant(''))).toBe(true);
    expect(Number.isNaN(parseInstant(undefined))).toBe(true);
    expect(Number.isNaN(parseInstant('nope'))).toBe(true);
  });
});
