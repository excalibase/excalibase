import { describe, expect, test } from 'vitest';
import { relativeTime } from './relativeTime';

const now = Date.parse('2026-11-27T12:00:00Z');

describe('relativeTime', () => {
  test.each([
    ['2026-11-27T11:59:40Z', 'just now'],
    ['2026-11-27T11:55:00Z', '5 min ago'],
    ['2026-11-27T10:00:00Z', '2 h ago'],
    ['2026-11-25T12:00:00Z', '2 d ago'],
    ['2026-11-27T12:00:30Z', 'just now'],
  ])('%s reads %s', (when, text) => {
    expect(relativeTime(when, now)).toBe(text);
  });

  test('a time that cannot be read says unknown, never NaN', () => {
    expect(relativeTime('yesterday', now)).toBe('unknown');
    expect(relativeTime(undefined, now)).toBe('unknown');
  });
});
