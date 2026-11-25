import { cn } from '../../utils/cn';
import type { AccountStatus } from '../../api/adminConsole';

const LOOK: Record<AccountStatus, { label: string; tone: string }> = {
  active: { label: 'Active', tone: 'bg-green-500/10 text-green-400 border-green-500/30' },
  suspended: { label: 'Suspended', tone: 'bg-red-500/10 text-red-400 border-red-500/30' },
  disabled: { label: 'Disabled', tone: 'bg-amber-500/10 text-amber-400 border-amber-500/30' },
};

// The status an account's data wears everywhere in the console: a project of a
// suspended owner reads "Suspended", never the database's own "Active".
export function AccountStatusBadge({ status, className }: { readonly status: AccountStatus; readonly className?: string }) {
  const look = LOOK[status] ?? LOOK.disabled;
  return <span className={cn('inline-flex px-2.5 py-1 text-xs font-medium rounded-full border', look.tone, className)}>{look.label}</span>;
}
