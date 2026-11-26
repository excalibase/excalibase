import { describe, expect, test } from 'vitest';
import { domainVerdict } from './domainPill';

describe('domainVerdict', () => {
  test.each([
    [{ accounts: 29, suspended: 29 }, 'spam'],
    [{ accounts: 10, suspended: 9 }, 'spam'],
    [{ accounts: 146, suspended: 24 }, 'mixed'],
    [{ accounts: 20, suspended: 3 }, 'mixed'],
    [{ accounts: 16, suspended: 2 }, null],
    [{ accounts: 12, suspended: 0 }, null],
    [{ accounts: 0, suspended: 0 }, null],
  ])('%j reads %s', (stat, verdict) => {
    expect(domainVerdict(stat)).toBe(verdict);
  });
});
