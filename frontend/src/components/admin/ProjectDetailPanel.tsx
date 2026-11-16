import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Loader2, X } from 'lucide-react';
import { getProjectDetail } from '../../api/adminConsole';
import type { AppView } from '../../api/adminConsole';
import { serverErrorMessage } from '../../utils/serverError';
import { StatusBadge } from '../shared/StatusBadge';
import { Button } from '../Button';
import { AccountStatusBadge } from './AccountStatusBadge';
import { ReasonDialog } from './ReasonDialog';

const running = (app: AppView) => `${app.runningReplicas ?? 'unknown'} / ${app.replicas}${app.runningReplicas == null ? '' : ' running'}`;

// A project as an operator needs it: who owns it, what it runs, and whether
// that is up. The reason is asked for before anything is read, because the
// server logs the opening against it.
export function ProjectDetailPanel({ projectId, onClose }: { readonly projectId: string; readonly onClose: () => void }) {
  const [reason, setReason] = useState<string | null>(null);
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'console', 'project', projectId, reason],
    queryFn: () => getProjectDetail(projectId, reason ?? ''),
    enabled: reason !== null,
    staleTime: 0,
    retry: false,
  });

  if (reason === null) {
    return (
      <ReasonDialog
        title="Open project data"
        description="You are about to look at a customer's project. Say why; it is logged."
        confirmLabel="Open project"
        onConfirm={setReason}
        onCancel={onClose}
      />
    );
  }
  return (
    <div className="fixed inset-0 z-40 overflow-auto bg-black/50 p-4">
      <div className="max-w-4xl mx-auto bg-surface-card border border-border-primary rounded-xl p-6 space-y-5">
        <div className="flex items-start justify-between">
          <h2 className="text-lg font-semibold">{data ? data.projectName || data.projectId : projectId}</h2>
          <Button variant="ghost" size="sm" aria-label="Close" onClick={onClose}>
            <X className="w-4 h-4" />
          </Button>
        </div>
        {isLoading && <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />}
        {error && <p role="alert" className="text-sm text-red-400">{serverErrorMessage(error, 'The project could not be read')}</p>}
        {data && (
          <>
            <dl className="grid grid-cols-2 md:grid-cols-4 gap-3 text-sm">
              <Fact label="Owner">
                <div>{data.ownerEmail}</div>
                <AccountStatusBadge status={data.ownerStatus} />
                {data.ownerSuspendedReason && <div className="text-xs text-text-tertiary mt-1">{data.ownerSuspendedReason}</div>}
              </Fact>
              <Fact label="Org">
                <div>{data.orgName}</div>
                <div className="text-xs text-text-tertiary uppercase">{data.tier}</div>
              </Fact>
              <Fact label="Database">
                {data.noDatabase ? (
                  <span className="text-text-tertiary">none</span>
                ) : (
                  <>
                    <div>{data.databaseType} {data.databaseVersion}</div>
                    {data.ownerStatus === 'suspended' ? <AccountStatusBadge status="suspended" /> : <StatusBadge status={data.databaseStatus} />}
                    {data.failureReason && <div className="text-xs text-red-400 mt-1">{data.failureReason}</div>}
                  </>
                )}
              </Fact>
              <Fact label="Database usage">
                <div>{data.cpuCores ? `${data.cpuCores.toFixed(2)} cores` : '—'}</div>
                <div>{data.memoryMb ? `${Math.round(data.memoryMb)} MiB` : '—'}</div>
              </Fact>
            </dl>
            <AppsTable apps={data.apps} />
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-sm">
              <List title="Sites" items={data.sites.map((site) => ({ key: site.id, text: site.name, hint: site.hostname }))} />
              <List title="Functions" items={data.functions.map((fn) => ({ key: fn.id, text: fn.id, hint: fn.active ? 'active' : 'inactive' }))} />
            </div>
            <List title="Recent audit events" items={data.audit.map((event, index) => ({ key: `${index}`, text: event.action, hint: event.details ?? event.userId ?? '' }))} />
          </>
        )}
      </div>
    </div>
  );
}

function Fact({ label, children }: { readonly label: string; readonly children: React.ReactNode }) {
  return (
    <div className="bg-bg-secondary border border-border-primary rounded-lg p-3">
      <dt className="text-xs uppercase tracking-wide text-text-tertiary">{label}</dt>
      <dd className="mt-1 space-y-1">{children}</dd>
    </div>
  );
}

function AppsTable({ apps }: { readonly apps: AppView[] }) {
  if (apps.length === 0) return <p className="text-sm text-text-secondary">This project runs no apps.</p>;
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-text-tertiary border-b border-border-primary">
          <th className="text-left py-2 font-medium">App</th>
          <th className="text-left py-2 font-medium">Image</th>
          <th className="text-left py-2 font-medium">Replicas</th>
          <th className="text-left py-2 font-medium">Status</th>
          <th className="text-left py-2 font-medium">Size per replica</th>
        </tr>
      </thead>
      <tbody>
        {apps.map((app) => (
          <tr key={app.id} className="border-b border-border-primary last:border-b-0">
            <td className="py-2 font-medium">{app.name}</td>
            <td className="py-2 font-mono text-xs">{app.image}</td>
            <td className="py-2">{running(app)}</td>
            <td className="py-2"><StatusBadge status={app.status} /></td>
            <td className="py-2 text-text-secondary">{app.cpuMilliPerReplica}m · {app.memoryMibPerReplica} MiB</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function List({ title, items }: { readonly title: string; readonly items: { key: string; text: string; hint: string }[] }) {
  return (
    <div>
      <h3 className="text-xs uppercase tracking-wide text-text-tertiary mb-1">{title}</h3>
      {items.length === 0 && <p className="text-text-tertiary">None.</p>}
      <ul className="space-y-0.5">
        {items.map((item) => (
          <li key={item.key} className="flex justify-between gap-3">
            <span>{item.text}</span>
            <span className="text-text-tertiary truncate">{item.hint}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
