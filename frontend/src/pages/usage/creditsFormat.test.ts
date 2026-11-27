import { describe, expect, test } from 'vitest';
import { dollars, microsFromDollars, daysLeft, recentMonths, trimTrailingZeros } from './creditsFormat';

describe('credit amounts', () => {
  test('write whole dollars, cents and sub-cent amounts exactly', () => {
    expect(dollars(300_000_000)).toBe('$300.00');
    expect(dollars(4_110)).toBe('$0.00411');
    expect(dollars(-2_250_000)).toBe('-$2.25');
    expect(dollars(1_234_567_000_000)).toBe('$1,234,567.00');
  });

  test('read a typed amount into micros without floats', () => {
    expect(microsFromDollars('12.5')).toBe(12_500_000);
    expect(microsFromDollars(' 1000 ')).toBe(1_000_000_000);
    expect(microsFromDollars('0.000001')).toBe(1);
    for (const bad of ['', '0', '0.0', '-5', '1e3', '1,000', '1.1234567', 'abc', '.5']) expect(microsFromDollars(bad)).toBeNull();
  });

  test('count days left upward and stop at zero', () => {
    const now = new Date('2026-12-01T12:00:00Z');
    expect(daysLeft('2026-12-02T11:00:00Z', now)).toBe(1);
    expect(daysLeft('2026-12-31T12:00:00Z', now)).toBe(30);
    expect(daysLeft('2026-11-01T00:00:00Z', now)).toBe(0);
    expect(daysLeft(null, now)).toBeNull();
    expect(daysLeft('garbage', now)).toBeNull();
  });

  test('list UTC months across a year boundary', () => {
    expect(recentMonths(new Date('2026-01-15T00:00:00Z'), 3)).toEqual(['2026-01', '2025-12', '2025-11']);
  });
});

describe('trimTrailingZeros', () => {
  test('drops only the zeros at the end', () => {
    expect(trimTrailingZeros('120300')).toBe('1203');
    expect(trimTrailingZeros('000000')).toBe('');
    expect(trimTrailingZeros('')).toBe('');
    expect(trimTrailingZeros('105')).toBe('105');
  });

  test('a very long run of zeros is linear work, not a stall', () => {
    const long = `1${'0'.repeat(200_000)}`;
    const started = performance.now();
    expect(trimTrailingZeros(long)).toBe('1');
    expect(trimTrailingZeros(`${'0'.repeat(200_000)}x`)).toHaveLength(200_001);
    expect(performance.now() - started).toBeLessThan(500);
  });
});
