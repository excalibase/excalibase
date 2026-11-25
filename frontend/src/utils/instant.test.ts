import { afterEach, describe, expect, test, vi } from 'vitest';
import { formatInstant, parseInstant } from './instant';

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

describe('formatInstant', () => {
  test('shows a refused time as unknown and warns once per value', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const zoneless = '2026-11-25T03:00:00.5';
    expect(formatInstant(zoneless)).toBe('unknown');
    expect(formatInstant(zoneless, 'date')).toBe('unknown');
    expect(formatInstant(zoneless, 'time')).toBe('unknown');
    expect(formatInstant(undefined)).toBe('unknown');
    expect(warn).toHaveBeenCalledTimes(1);
  });

  test('shows a zoned time in the viewer zone', () => {
    const at = new Date('2026-11-25T02:30:00Z');
    expect(formatInstant('2026-11-25T02:30:00Z')).toBe(at.toLocaleString());
    expect(formatInstant('2026-11-25T02:30:00Z', 'date')).toBe(at.toLocaleDateString());
    expect(formatInstant('2026-11-25T02:30:00Z', 'time')).toBe(at.toLocaleTimeString());
  });
});
