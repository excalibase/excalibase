import { Navigate } from 'react-router-dom';
import { useAuthStore } from '../stores/auth-store';
import { useClusterCapacity } from '../hooks/useAdmin';
import { AdminConsoleV2 } from '../components/admin/v2/AdminConsoleV2';
import { TierConfigTable } from '../components/TierConfigTable';
import { StorageBudgetCard } from '../components/StorageBudgetCard';
import { SignInProviders } from '../components/SignInProviders';
import { Loader2, Cpu, MemoryStick } from 'lucide-react';

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
  return <AdminPageBody canMutate={canMutate} />;
}

// The tabbed console is the admin layout for every install: it is staff-only
// UI, so it does not wait for the customer-facing studio_v2 flag.
function AdminPageBody({ canMutate }: { readonly canMutate: boolean }) {
  return (
    <AdminConsoleV2
      canMutate={canMutate}
      platform={(
        <>
          <CapacityWidget />
          <StorageBudgetCard />
          <TierConfigTable canMutate={canMutate} />
          {canMutate && <SignInProviders />}
        </>
      )}
    />
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
