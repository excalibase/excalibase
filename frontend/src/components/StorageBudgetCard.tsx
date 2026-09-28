import { HardDrive, Loader2 } from 'lucide-react';
import { useStorageBudget, type StorageBudget } from '../hooks/useAdmin';
import { apiErrorMessage } from '../api/apps';
import { formatDiskBytes } from '../utils/diskSize';
import { cn } from '../utils/cn';

type BudgetLevel = 'ok' | 'warning' | 'critical';

const LEVEL_COLOR: Record<BudgetLevel, string> = {
  ok: 'bg-accent-primary',
  warning: 'bg-yellow-500',
  critical: 'bg-red-500',
};

const budgetLevel = (percent: number): BudgetLevel => {
  if (percent >= 80) return 'critical';
  if (percent >= 70) return 'warning';
  return 'ok';
};

const Shell = ({ children }: { readonly children: React.ReactNode }) => (
  <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
    <h2 className="flex items-center gap-2 text-lg font-semibold">
      <HardDrive className="w-4 h-4" /> Storage budget
    </h2>
    {children}
  </div>
);

function Breakdown({ report }: { readonly report: StorageBudget }) {
  const parts: Array<[string, string, number]> = [
    ['tenants', 'Tenant volumes', report.tenantBytes],
    ['platform', 'Platform volumes', report.platformBytes],
    ['pending', 'Pending claims', report.pendingBytes],
    ['free', 'Free in budget', report.freeBytes],
  ];
  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
      {parts.map(([key, label, bytes]) => (
        <div key={key} className="bg-bg-secondary border border-border-primary rounded-lg p-3">
          <div className="text-xs uppercase tracking-wide text-text-tertiary">{label}</div>
          <div className="mt-1 text-lg font-semibold" data-testid={`storage-budget-${key}`}>
            {formatDiskBytes(bytes)}
          </div>
        </div>
      ))}
    </div>
  );
}

function Metered({ report }: { readonly report: StorageBudget }) {
  const level = budgetLevel(report.usedPercent);
  const percent = Math.round(report.usedPercent);
  return (
    <>
      <div>
        <div className="flex items-center justify-between mb-1 text-sm text-text-secondary">
          <span data-testid="storage-budget-used">
            {formatDiskBytes(report.allocatedBytes)} of {formatDiskBytes(report.budgetBytes)}{' '}
            reserved ({percent}%)
          </span>
          <span className="text-xs text-text-tertiary" data-testid="storage-budget-share">
            Budget is {report.percent}% of {formatDiskBytes(report.capacityBytes)} capacity
          </span>
        </div>
        <div
          className="h-2 bg-bg-secondary rounded-full overflow-hidden"
          data-testid="storage-budget-bar"
          data-level={level}
        >
          <div
            className={cn('h-full transition-all', LEVEL_COLOR[level])}
            style={{ width: `${Math.min(100, percent)}%` }}
          />
        </div>
      </div>
      <Breakdown report={report} />
    </>
  );
}

// Every volume's reservation against the share of the node's storage volumes may take;
// a disk or database that would pass it is refused.
export function StorageBudgetCard() {
  const { data: report, isLoading, error } = useStorageBudget();
  if (isLoading) {
    return (
      <Shell>
        <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
      </Shell>
    );
  }
  if (error || !report) {
    return (
      <Shell>
        <p role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, "The platform's storage could not be read")}
        </p>
      </Shell>
    );
  }
  if (!report.enabled) {
    return (
      <Shell>
        <p className="text-sm text-text-secondary" data-testid="storage-budget-unmetered">
          Not metered: this installation does not limit how much storage volumes reserve.
        </p>
      </Shell>
    );
  }
  return (
    <Shell>
      <Metered report={report} />
    </Shell>
  );
}
