import type { DomainStat } from '../../../api/adminAnalytics';

// Share of a domain's accounts that were suspended: nearly all marks a spam
// domain, a real share marks a mixed one worth a look.
export const SPAM_SHARE = 0.9;
export const MIXED_SHARE = 0.15;

export type DomainVerdict = 'spam' | 'mixed' | null;

export function domainVerdict(stat: Pick<DomainStat, 'accounts' | 'suspended'>): DomainVerdict {
  if (stat.accounts === 0) return null;
  const share = stat.suspended / stat.accounts;
  if (share >= SPAM_SHARE) return 'spam';
  if (share >= MIXED_SHARE) return 'mixed';
  return null;
}
