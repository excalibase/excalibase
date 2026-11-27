import { Link } from 'react-router-dom';
import { AlertTriangle, Loader2 } from 'lucide-react';
import { serverErrorMessage } from '../../utils/serverError';
import { useFailedProjects, useOverview, useSuspensions } from '../../hooks/useAdminConsole';
import { FailedColumn, SignupsColumn, SuspendedColumn } from './attention/AttentionColumns';
import type { ProjectBrief } from '../../api/adminConsole';
import { AccountStatusBadge } from './AccountStatusBadge';

const cores = (value?: number) => (value ? `${value.toFixed(2)} cores` : '—');
const mib = (value?: number) => (value ? `${Math.round(value)} MiB` : '—');
const rate = (value?: number) => (value ? `${(value / 1024).toFixed(1)} KiB/s` : '—');

function Section({ title, children }: { readonly title: string; readonly children: React.ReactNode }) {
  return (
    <div className="bg-bg-secondary border border-border-primary rounded-lg p-4 space-y-2">
      <h3 className="text-xs uppercase tracking-wide text-text-tertiary">{title}</h3>
      {children}
    </div>
  );
}

function TopList({ title, rows, value }: { readonly title: string; readonly rows: ProjectBrief[]; readonly value: (row: ProjectBrief) => string }) {
  return (
    <Section title={title}>
      {rows.length === 0 && <p className="text-sm text-text-tertiary">No live samples.</p>}
      <ul className="space-y-1.5">
        {rows.map((row) => (
          <li key={row.projectId} className="flex items-center justify-between gap-2 text-sm">
            <div className="min-w-0">
              <Link className="font-medium hover:underline" to={`/admin?project=${row.projectId}`}>
                {row.projectName || row.projectId}
              </Link>
              <div className="text-xs text-text-tertiary truncate">{row.ownerEmail}</div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {row.ownerStatus !== 'active' && <AccountStatusBadge status={row.ownerStatus} />}
              <span className="text-text-secondary">{value(row)}</span>
            </div>
          </li>
        ))}
      </ul>
    </Section>
  );
}

// First on the page: what an operator should look at before anything else.
export function NeedsAttention() {
  const { data, isLoading, error } = useOverview();
  const suspended = useSuspensions();
  const failed = useFailedProjects();
  if (isLoading) {
    return (
      <div className="bg-surface-card border border-border-primary rounded-xl p-6 flex justify-center">
        <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
      </div>
    );
  }
  if (error || !data) {
    return (
      <div className="bg-surface-card border border-border-primary rounded-xl p-6">
        <p role="alert" className="text-sm text-red-400">{serverErrorMessage(error, 'The overview could not be read')}</p>
      </div>
    );
  }
  const quiet = suspended.isSuccess && failed.isSuccess && (suspended.data?.pages[0]?.total ?? 0) === 0 && (failed.data?.pages[0]?.total ?? 0) === 0 && data.signups24h === 0;
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
      <h2 className="flex items-center gap-2 text-lg font-semibold">
        <AlertTriangle className="w-4 h-4" /> Needs attention
      </h2>
      {quiet && <p className="text-sm text-text-secondary">Nothing needs attention.</p>}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-3 items-start">
        <SignupsColumn signups={data.signups24h} domains={data.signupDomains} />
        <SuspendedColumn query={suspended} />
        <FailedColumn query={failed} />
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-3">
        <TopList title="Top CPU" rows={data.topCpu} value={(row) => cores(row.cpuCores)} />
        <TopList title="Top memory" rows={data.topMemory} value={(row) => mib(row.memoryMb)} />
        <div className="space-y-2">
          <TopList title="Top egress" rows={data.topEgress} value={(row) => rate(row.egressBytesPerSec)} />
          {data.topEgress.length === 0 && (
            <p className="text-xs text-text-tertiary">Egress is recorded on single-host installs only.</p>
          )}
        </div>
      </div>
    </div>
  );
}
