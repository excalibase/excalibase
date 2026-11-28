import type { CreditOverview, CreditZero } from '../../../api/credits';
import { daysLeft, dollars } from '../../../pages/usage/creditsFormat';
import { parseInstant } from '../../../utils/instant';
import { ADD_CREDIT_TEXT } from '../../../utils/support';

export interface CreditNotice {
  tone: 'bad' | 'warn';
  text: string;
}

// Credit expiring this soon is worth a banner of its own.
const EXPIRY_WARNING_DAYS = 7;

const plural = (count: number) => `${count} day${count === 1 ? '' : 's'}`;
const dateOf = (instant: string) => new Date(parseInstant(instant)).toLocaleDateString();

function usedUp(zero: CreditZero, now: Date): CreditNotice {
  const start = 'Your credit is used up.';
  const ends = zero.graceEndsAt ? parseInstant(zero.graceEndsAt) : Number.NaN;
  if (Number.isNaN(ends) || ends > now.getTime()) {
    const when = zero.graceEndsAt ? ` on ${dateOf(zero.graceEndsAt)} (in ${plural(daysLeft(zero.graceEndsAt, now) ?? 0)})` : ' soon';
    return { tone: 'bad', text: `${start} New services and growth are blocked now, and compute above your free allowance pauses${when}. ${ADD_CREDIT_TEXT}` };
  }
  if (zero.paused.length === 0) {
    return { tone: 'bad', text: `${start} New services and growth are blocked. Nothing is paused: what you run fits your free allowance. ${ADD_CREDIT_TEXT}` };
  }
  return { tone: 'bad', text: `${start} Compute above your free allowance is paused and your data is kept. ${ADD_CREDIT_TEXT} Paused services then start again by themselves.` };
}

// The one thing the global banner says about an org's credit, or nothing: used up,
// then expiring within a week, then running low. A read that did not carry the
// section is treated as nothing to say, never as an error.
export function creditNotice(overview: CreditOverview | undefined, now: Date): CreditNotice | undefined {
  const zero = overview?.zero;
  if (!overview?.metering || !zero || typeof zero !== 'object') return undefined;
  if (zero.atZero) return usedUp(zero, now);
  const left = daysLeft(overview.nextExpiry, now);
  if (overview.nextExpiry && left !== null && left <= EXPIRY_WARNING_DAYS) {
    return { tone: 'warn', text: `Your credit expires on ${dateOf(overview.nextExpiry)} (in ${plural(left)}). After that, new services are blocked and, after a grace period, compute above your free allowance pauses.` };
  }
  if (zero.lowCredit) return { tone: 'warn', text: `Your credit is running low: ${dollars(overview.balanceMicros)} left.` };
  return undefined;
}
