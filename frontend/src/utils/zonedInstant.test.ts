import { describe, expect, it } from 'vitest';
import { toLocalInput, toZonedInstant } from './zonedInstant';

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

describe('toLocalInput', () => {
  it('shows an instant as the datetime-local value the browser displays', () => {
    const local = toLocalInput('2026-10-11T12:00:30Z');
    expect(new Date(local).toISOString()).toBe('2026-10-11T12:00:30.000Z');
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/);
  });
});
