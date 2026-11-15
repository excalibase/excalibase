import { useState } from 'react';
import { Navigate, useSearchParams } from 'react-router-dom';
import { useAuthStore } from '../stores/auth-store';
import { useAdminProjects, useClusterCapacity, useForceDropProject } from '../hooks/useAdmin';
import { Button } from '../components/Button';
import { StatusBadge } from '../components/shared/StatusBadge';
import { AccountStatusBadge } from '../components/admin/AccountStatusBadge';
import { NeedsAttention } from '../components/admin/NeedsAttention';
import { OrgsTable } from '../components/admin/OrgsTable';
import { ProjectDetailPanel } from '../components/admin/ProjectDetailPanel';
import { ReasonDialog } from '../components/admin/ReasonDialog';
import { UsersTable } from '../components/admin/UsersTable';
import { TierConfigTable } from '../components/TierConfigTable';
import { StorageBudgetCard } from '../components/StorageBudgetCard';
import { SignInProviders } from '../components/SignInProviders';
import { Loader2, Trash2, Cpu, MemoryStick, Eye } from 'lucide-react';
import { serverErrorMessage } from '../utils/serverError';

// PlatformAdminPage is the operator dashboard. Surfaces cluster capacity,
// every project across every org, and lets platform_admin force-drop a
// project (bypassing deletion_protection) or revoke an entire org.
//
// Gated to platform_admin only; platform_operator has read-only access via
// the table but the destructive buttons stay disabled. Anything else is
// redirected to the home page.
export function PlatformAdminPage() {
  const user = useAuthStore((s) => s.user);
  if (user == null || (user.role !== 'platform_admin' && user.role !== 'platform_operator')) {
    return <Navigate to="/" replace />;
  }
  const canMutate = user.role === 'platform_admin';
  return (
    <div className="max-w-7xl mx-auto space-y-6">
      {canMutate && <NeedsAttention />}
      <CapacityWidget />
      <StorageBudgetCard />
      <TierConfigTable canMutate={canMutate} />
      <ProjectsTable canMutate={canMutate} />
      {canMutate && <UsersTable />}
      <OrgsTable canMutate={canMutate} />
      {canMutate && <SignInProviders />}
    </div>
  );
}

function CapacityWidget() {
  const { data: cap, isLoading } = useClusterCapacity();
  if (isLoading || !cap) {
    return (
      <div className="bg-surface-card border border-border-primary rounded-xl p-6 flex items-center justify-center min-h-[120px]">
        <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
      </div>
    );
  }
  const cpuPct = cap.usableCpuMilli > 0 ? Math.round((cap.requestedCpuMilli / cap.usableCpuMilli) * 100) : 0;
  const memPct = cap.usableMemBytes > 0 ? Math.round((cap.requestedMemBytes / cap.usableMemBytes) * 100) : 0;
  const gib = (b: number) => (b / 1024 / 1024 / 1024).toFixed(1);
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Cluster capacity</h2>
        <span className="text-xs text-text-tertiary">
          {cap.headroomPercent}% safety headroom · {cap.projects.total} projects provisioned
        </span>
      </div>
      <div className="grid grid-cols-2 gap-6">
        <CapacityBar
          icon={<Cpu className="w-4 h-4" />}
          label="CPU"
          used={`${(cap.requestedCpuMilli / 1000).toFixed(2)} / ${(cap.usableCpuMilli / 1000).toFixed(2)} cores`}
          pct={cpuPct}
        />
        <CapacityBar
          icon={<MemoryStick className="w-4 h-4" />}
          label="Memory"
          used={`${gib(cap.requestedMemBytes)} / ${gib(cap.usableMemBytes)} GiB`}
          pct={memPct}
        />
      </div>
      <div className="grid grid-cols-3 gap-3 pt-2">
        {(['free', 'standard', 'enterprise'] as const).map((tier) => {
          const t = cap.tiers[tier];
          if (!t) return null;
          return (
            <div key={tier} className="bg-bg-secondary border border-border-primary rounded-lg p-3">
              <div className="flex items-baseline justify-between">
                <span className="text-xs uppercase tracking-wide text-text-tertiary">{tier}</span>
                <span className="text-xs text-text-tertiary">{t.currentlyProvisioned} active</span>
              </div>
              <div className="mt-1 text-2xl font-semibold">{t.projectsCanFit}</div>
              <div className="text-xs text-text-tertiary">slots free · limited by {t.limitedBy}</div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function capacityBarColor(pct: number): string {
  if (pct >= 80) return 'bg-red-500';
  if (pct >= 60) return 'bg-yellow-500';
  return 'bg-accent-primary';
}

function CapacityBar({ icon, label, used, pct }: { readonly icon: React.ReactNode; readonly label: string; readonly used: string; readonly pct: number }) {
  const color = capacityBarColor(pct);
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <span className="flex items-center gap-1.5 text-sm text-text-secondary">
          {icon}
          {label}
        </span>
        <span className="text-xs text-text-tertiary">{used}</span>
      </div>
      <div className="h-2 bg-bg-secondary rounded-full overflow-hidden">
        <div className={`h-full ${color} transition-all`} style={{ width: `${Math.min(100, pct)}%` }} />
      </div>
    </div>
  );
}

function ProjectsTable({ canMutate }: { readonly canMutate: boolean }) {
  const { data: projects = [], isLoading } = useAdminProjects();
  const drop = useForceDropProject();
  const [params, setParams] = useSearchParams();
  const openProject = params.get('project');
  const showProject = (projectId: string | null) => {
    const next = new URLSearchParams(params);
    if (projectId) next.set('project', projectId);
    else next.delete('project');
    setParams(next, { replace: true });
  };

  const [dropping, setDropping] = useState<{ projectId: string; name: string } | null>(null);

  const confirmDrop = async (reason: string) => {
    if (!dropping || !canMutate) return;
    try {
      await drop.mutateAsync({ projectId: dropping.projectId, reason });
      setDropping(null);
    } catch {
      // the dialog stays open and shows drop.error
    }
  };

  return (
    <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
      <div className="px-6 py-4 border-b border-border-primary flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">All projects</h2>
          <p className="text-xs text-text-tertiary mt-0.5">Cross-org operator view. Drop bypasses deletion protection.</p>
        </div>
        <span className="text-sm text-text-secondary">{projects.length}</span>
      </div>
      {isLoading && (
        <div className="flex items-center justify-center py-12">
          <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
        </div>
      )}
      {!isLoading && projects.length === 0 && (
        <div className="text-center py-12 text-text-secondary">No projects provisioned.</div>
      )}
      {!isLoading && projects.length > 0 && (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
              <th className="text-left px-6 py-3 font-medium">Project</th>
              <th className="text-left px-6 py-3 font-medium">Org and owner</th>
              <th className="text-left px-6 py-3 font-medium">Tier</th>
              <th className="text-left px-6 py-3 font-medium">Status</th>
              <th className="text-right px-6 py-3 font-medium">CPU</th>
              <th className="text-right px-6 py-3 font-medium">Memory</th>
              <th className="px-6 py-3" />
            </tr>
          </thead>
          <tbody>
            {projects.map((p) => (
              <tr key={p.projectId} className="border-b border-border-primary last:border-b-0 hover:bg-bg-hover">
                <td className="px-6 py-3">
                  <div className="font-medium">{p.projectName}</div>
                  <div className="text-xs text-text-tertiary font-mono">{p.projectId}</div>
                </td>
                <td className="px-6 py-3 text-text-secondary">
                  <div>{p.orgName || p.orgId}</div>
                  {p.ownerEmail && <div className="text-xs text-text-tertiary">{p.ownerEmail}</div>}
                </td>
                <td className="px-6 py-3 text-text-secondary uppercase text-xs">{p.tier}</td>
                <td className="px-6 py-3">
                  {p.ownerSuspended ? <AccountStatusBadge status="suspended" /> : <StatusBadge status={p.status} />}
                </td>
                <td className="px-6 py-3 text-right text-text-secondary">
                  {p.cpuCores == null ? '—' : p.cpuCores.toFixed(2)}
                </td>
                <td className="px-6 py-3 text-right text-text-secondary">
                  {p.memBytes == null ? '—' : `${(p.memBytes / 1024 / 1024).toFixed(0)} MiB`}
                </td>
                <td className="px-6 py-3 text-right space-x-2">
                  {canMutate && (
                    <Button size="sm" variant="secondary" onClick={() => showProject(p.projectId)}>
                      <Eye className="w-3.5 h-3.5 mr-1" />
                      Open
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="danger"
                    disabled={!canMutate || drop.isPending}
                    onClick={() => { drop.reset(); setDropping({ projectId: p.projectId, name: p.projectName }); }}
                  >
                    <Trash2 className="w-3.5 h-3.5 mr-1" />
                    Drop
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {dropping && (
        <ReasonDialog
          title={`Force-drop project "${dropping.name}"`}
          description="Bypasses deletion protection and cannot be undone."
          confirmLabel="Drop"
          danger
          busy={drop.isPending}
          error={drop.error ? serverErrorMessage(drop.error, 'Drop failed') : undefined}
          onConfirm={confirmDrop}
          onCancel={() => setDropping(null)}
        />
      )}
      {canMutate && openProject && <ProjectDetailPanel key={openProject} projectId={openProject} onClose={() => showProject(null)} />}
    </div>
  );
}
