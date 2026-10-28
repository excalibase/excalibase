import { useState } from 'react';
import { Navigate } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { useAuthStore } from '../stores/auth-store';
import { useHostMonitoring, useMonitoredContainers } from '../hooks/useMonitoring';
import type { MonitoringRange } from '../api/monitoring';
import { RangePicker } from '../components/monitoring/RangePicker';
import { HostCards } from '../components/monitoring/HostCards';
import { HostCharts } from '../components/monitoring/HostCharts';
import { ContainersTable } from '../components/monitoring/ContainersTable';
import { AlertSettingsCard } from '../components/monitoring/AlertSettingsCard';

const PLATFORM_ADMIN_ROLES = new Set(['platform_admin', 'platform_operator']);

function Loading({ label }: { readonly label: string }) {
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl flex items-center justify-center gap-3 py-12">
      <Loader2 className="w-5 h-5 animate-spin text-purple-400" />
      <span className="text-sm text-text-secondary">{label}</span>
    </div>
  );
}

function HostSection({ range }: { readonly range: MonitoringRange }) {
  const { data, isLoading, isError } = useHostMonitoring(range);
  if (isLoading) return <Loading label="Fetching host metrics..." />;
  if (isError || !data) {
    return (
      <div className="text-center py-12 text-text-secondary">Host metrics could not be loaded.</div>
    );
  }
  return (
    <>
      {data.current ? (
        <HostCards sample={data.current} />
      ) : (
        <div className="text-center py-12 text-text-secondary">No host sample yet.</div>
      )}
      {data.points.length > 0 && <HostCharts points={data.points} range={range} />}
    </>
  );
}

function ContainersSection({ range }: { readonly range: MonitoringRange }) {
  const { data, isLoading, isError } = useMonitoredContainers();
  let body: React.ReactNode;
  if (isLoading) {
    body = (
      <div className="flex justify-center py-10">
        <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />
      </div>
    );
  } else if (isError || !data) {
    body = (
      <p className="text-center py-10 text-sm text-text-secondary">
        Containers could not be loaded.
      </p>
    );
  } else {
    body = <ContainersTable containers={data.containers} range={range} />;
  }
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
      <div className="px-6 py-4 border-b border-border-primary flex items-center justify-between">
        <h2 className="text-lg font-semibold">Containers</h2>
        {data?.timestamp && (
          <span className="text-xs text-text-tertiary">
            sampled {new Date(data.timestamp).toLocaleTimeString()}
          </span>
        )}
      </div>
      {body}
    </div>
  );
}

// The single host running a Docker/Podman install: its own figures, every
// container on it, and the alerts that watch them (EXC-670).
export function HostMonitoringPage() {
  const user = useAuthStore((s) => s.user);
  const [range, setRange] = useState<MonitoringRange>('1h');
  if (user == null || !PLATFORM_ADMIN_ROLES.has(user.role)) {
    return <Navigate to="/" replace />;
  }
  return (
    <div className="max-w-7xl mx-auto space-y-6">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <div>
          <h2 className="text-lg font-semibold text-text-primary">Host</h2>
          <p className="text-sm text-text-secondary">
            CPU, memory, disk, network and containers of this host, sampled every minute.
          </p>
        </div>
        <RangePicker value={range} onChange={setRange} />
      </div>
      <HostSection range={range} />
      <ContainersSection range={range} />
      <AlertSettingsCard canMutate={user.role === 'platform_admin'} />
    </div>
  );
}
