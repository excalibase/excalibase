import { describe, expect, it } from 'vitest';
import { toZonedInstant } from './zonedInstant';

describe('toZonedInstant', () => {
  it('sends a datetime-local value as the instant the browser shows, with a zone', () => {
    expect(toZonedInstant('2026-05-04T03:30')).toBe(new Date(2026, 4, 4, 3, 30).toISOString());
    expect(toZonedInstant('2026-05-04T03:30:15')).toBe(new Date(2026, 4, 4, 3, 30, 15).toISOString());
    expect(toZonedInstant('2026-05-04T03:30')).toMatch(/Z$/);
  });

  it('leaves a blank value as no target', () => {
    expect(toZonedInstant('')).toBeUndefined();
    expect(toZonedInstant('   ')).toBeUndefined();
    expect(toZonedInstant(undefined)).toBeUndefined();
  });

  it('refuses a value that is not a time', () => {
    expect(() => toZonedInstant('yesterday')).toThrow();
  });
});
