import { describe, test, expect } from 'vitest';
import { creditNotice } from './creditNotice';
import { creditOverview } from '../../../test/homeFixtures';
import type { CreditOverview, CreditZero } from '../../../api/credits';

const NOW = new Date('2026-12-01T00:00:00Z');
const zero = (extra: Partial<CreditZero> = {}): CreditZero => ({ atZero: false, lowCredit: false, graceEndsAt: null, graceDays: 7, paused: [], ...extra });
const overview = (extra: object = {}) => creditOverview(extra) as unknown as CreditOverview;

describe('the credit banner', () => {
  test('says nothing while the credit is fine, unknown or not billed', () => {
    expect(creditNotice(undefined, NOW)).toBeUndefined();
    expect(creditNotice(overview({ zero: zero() }), NOW)).toBeUndefined();
    expect(creditNotice(overview({ zero: null }), NOW)).toBeUndefined();
    expect(creditNotice(overview({}), NOW)).toBeUndefined();
    expect(creditNotice(overview({ metering: false, zero: zero({ atZero: true, graceEndsAt: '2026-12-05T00:00:00Z' }) }), NOW)).toBeUndefined();
  });

  test('at $0 in the grace period it names the pause date and what is refused now', () => {
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: '2026-12-05T00:00:00Z' }) }), NOW, 'help@example.test');
    expect(notice?.tone).toBe('bad');
    expect(notice?.text).toMatch(/credit is used up/i);
    expect(notice?.text).toMatch(/new services and growth are blocked/i);
    expect(notice?.text).toContain(new Date('2026-12-05T00:00:00Z').toLocaleDateString());
    expect(notice?.text).toMatch(/4 days/);
    expect(notice?.text).toContain('help@example.test');
  });

  test('on the last day of the grace period it counts one day, not zero', () => {
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: '2026-12-01T10:00:00Z' }) }), NOW);
    expect(notice?.text).toMatch(/1 day\b/);
  });

  test('past the grace period it says compute is paused and the data kept', () => {
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: '2026-11-25T00:00:00Z', paused: [{ kind: 'app', projectId: 'p', serviceId: 'a', name: 'web', pausedAt: '2026-11-25T00:00:00Z' }] }) }), NOW, 'help@example.test');
    expect(notice?.tone).toBe('bad');
    expect(notice?.text).toMatch(/paused/i);
    expect(notice?.text).toMatch(/data is kept/i);
    expect(notice?.text).not.toMatch(/will pause/i);
    expect(notice?.text).toContain('Contact support to add credit: help@example.test');
    expect(notice?.text).not.toMatch(/upgrade/i);
  });

  test('without an address on the server it still says to contact support', () => {
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: '2026-12-05T00:00:00Z' }) }), NOW);
    expect(notice?.text).toContain('Contact support to add credit.');
    expect(notice?.text).not.toMatch(/@/);
  });

  test('past the grace period with nothing paused it does not say anything is paused', () => {
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: '2026-11-25T00:00:00Z', paused: [] }) }), NOW);
    expect(notice?.text).toMatch(/nothing is paused/i);
    expect(notice?.text).not.toMatch(/is paused and/i);
    expect(notice?.text).toMatch(/new services and growth are blocked/i);
  });

  test('at the very moment the grace ends the compute is paused', () => {
    const paused = [{ kind: 'database' as const, projectId: 'p', serviceId: 'p', name: 'db', pausedAt: NOW.toISOString() }];
    const notice = creditNotice(overview({ balanceMicros: 0, zero: zero({ atZero: true, graceEndsAt: NOW.toISOString(), paused }) }), NOW);
    expect(notice?.text).toMatch(/is paused and your data is kept/i);
  });

  test('low credit warns without alarming, with the amount left', () => {
    const notice = creditNotice(overview({ balanceMicros: 40_000_000, zero: zero({ lowCredit: true }) }), NOW);
    expect(notice?.tone).toBe('warn');
    expect(notice?.text).toContain('$40.00');
  });

  test('credit expiring within a week warns with the date', () => {
    const notice = creditNotice(overview({ nextExpiry: '2026-12-05T00:00:00Z', zero: zero() }), NOW);
    expect(notice?.tone).toBe('warn');
    expect(notice?.text).toContain(new Date('2026-12-05T00:00:00Z').toLocaleDateString());
    expect(creditNotice(overview({ nextExpiry: '2026-12-08T00:00:00Z', zero: zero() }), NOW)?.text).toMatch(/in 7 days/);
    expect(creditNotice(overview({ nextExpiry: '2026-12-09T00:00:00Z', zero: zero() }), NOW)).toBeUndefined();
  });

  test('used up outranks expiring, which outranks low', () => {
    const base = { balanceMicros: 1_000_000, nextExpiry: '2026-12-03T00:00:00Z' };
    expect(creditNotice(overview({ ...base, zero: zero({ lowCredit: true }) }), NOW)?.text).toMatch(/expires/i);
    expect(creditNotice(overview({ ...base, balanceMicros: 0, zero: zero({ atZero: true, lowCredit: true, graceEndsAt: '2026-12-08T00:00:00Z' }) }), NOW)?.text).toMatch(/used up/i);
  });

  test('a response with a missing or malformed zero section is ignored, never thrown on', () => {
    expect(creditNotice({ zero: 'oops' } as unknown as CreditOverview, NOW)).toBeUndefined();
    expect(creditNotice({} as CreditOverview, NOW)).toBeUndefined();
  });
});
