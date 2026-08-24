import { useCurrentMetrics, useMetricsHistory } from '../hooks/useMetrics';
import { useInstanceContext } from '../context/InstanceContext';
import { MetricCard } from '../components/shared/MetricCard';
import { Cpu, Server, HardDrive, Network, Zap, Timer, Loader2, AlertTriangle } from 'lucide-react';
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend,
} from 'recharts';

// SKELETON_KEYS provides stable React keys for the loading-state placeholder
// tiles. Static identifiers avoid the array-index-as-key smell (S6479).
const SKELETON_KEYS = ['cpu', 'mem', 'storage', 'conn', 'health', 'backup'] as const;

interface UsageBarProps {
  readonly value: number;
  readonly max: number;
  readonly color: string;
}

function UsageBar({ value, max, color }: UsageBarProps) {
  const pct = max > 0 ? Math.min((value / max) * 100, 100) : 0;
  return (
    <div className="w-full bg-bg-tertiary rounded-full h-2 mt-1">
      <div className={`h-2 rounded-full ${color}`} style={{ width: `${pct}%` }} />
    </div>
  );
}

export function MetricsPage() {
  const { projectId } = useInstanceContext();

  const { data: current, isLoading } = useCurrentMetrics(projectId);
  const { data: history } = useMetricsHistory(projectId, 30);

  const chartData = history?.metrics.filter(m => m.metricsAvailable).map((m) => ({
    time: new Date(m.timestamp).toLocaleTimeString(),
    cpu: Math.round(m.cpuUsagePercent ?? 0),
    memory: Math.round(m.memoryUsagePercent ?? 0),
  })) ?? [];

  return (
    <div className="max-w-7xl mx-auto space-y-6">
      {isLoading && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 lg:grid-cols-3 gap-4">
            {SKELETON_KEYS.map((k) => (
              <div key={k} className="bg-surface-card border border-border-primary rounded-xl p-5 animate-pulse">
                <div className="h-3 w-16 bg-bg-tertiary rounded mb-3" />
                <div className="h-7 w-20 bg-bg-tertiary rounded mb-2" />
                <div className="h-2 w-12 bg-bg-tertiary rounded" />
              </div>
            ))}
          </div>
          <div className="bg-surface-card border border-border-primary rounded-xl p-6 flex items-center justify-center gap-3 py-16">
            <Loader2 className="w-5 h-5 animate-spin text-purple-400" />
            <span className="text-sm text-text-secondary">Fetching metrics...</span>
          </div>
        </div>
      )}
      {!isLoading && current == null && (
        <div className="text-center py-20 text-text-secondary">No metrics data yet.</div>
      )}
      {!isLoading && current != null && !current.metricsAvailable && (
        <div className="flex flex-col items-center justify-center py-24 text-center gap-6">
          <div className="w-14 h-14 rounded-full bg-amber-500/10 flex items-center justify-center">
            <AlertTriangle className="w-7 h-7 text-amber-400" />
          </div>
          <div className="space-y-2">
            <h2 className="text-lg font-semibold text-text-primary">Metrics unavailable</h2>
            <p className="text-sm text-text-secondary max-w-md">{current.unavailableReason}</p>
          </div>
          <div className="bg-surface-card border border-border-primary rounded-xl p-5 text-left max-w-md w-full space-y-2">
            <p className="text-xs font-semibold text-text-secondary uppercase tracking-wide">How to enable metrics</p>
            <ol className="text-sm text-text-secondary space-y-1 list-decimal list-inside">
              <li>Provision your database with <code className="text-purple-400">metricsEnabled: true</code></li>
              <li>Ensure the CloudNativePG Prometheus exporter is running on port 9187</li>
              <li>Make sure your Kubernetes cluster is reachable</li>
            </ol>
          </div>
        </div>
      )}
      {!isLoading && current != null && current.metricsAvailable && (
        <>
          <div className="grid grid-cols-2 lg:grid-cols-3 gap-4">
            <MetricCard
              icon={<Cpu className="w-4 h-4" />}
              label="CPU"
              value={current.cpuUsageCores == null ? 'N/A' : `${current.cpuUsageCores.toFixed(2)} cores`}
              subtitle={current.cpuLimitCores == null ? undefined : `/ ${current.cpuLimitCores} limit (${current.instanceCount} pods)`}
              color="text-blue-400"
            />
            <MetricCard
              icon={<Server className="w-4 h-4" />}
              label="Memory"
              value={current.memoryUsageMB == null ? 'N/A' : `${current.memoryUsageMB} MB`}
              subtitle={current.memoryLimitMB == null ? undefined : `/ ${current.memoryLimitMB} MB limit`}
              color="text-purple-400"
            />
            <MetricCard
              icon={<HardDrive className="w-4 h-4" />}
              label="Storage"
              value={current.storageLimit ?? 'N/A'}
              subtitle={current.databaseSizeGB == null ? undefined : `${current.databaseSizeGB} GB used`}
              color="text-orange-400"
            />
            <MetricCard
              icon={<Network className="w-4 h-4" />}
              label="Connections"
              value={current.activeConnections ?? '---'}
              subtitle={`/ ${current.maxConnections ?? '---'} max`}
              color="text-emerald-400"
            />
            <MetricCard
              icon={<Zap className="w-4 h-4" />}
              label="Health"
              value={current.healthStatus ?? '---'}
              subtitle={`${current.instanceCount ?? 1} instance(s)`}
              color={current.healthStatus === 'HEALTHY' ? 'text-green-400' : 'text-red-400'}
            />
            <MetricCard
              icon={<Timer className="w-4 h-4" />}
              label="Last Backup"
              value={current.lastBackupTime ? new Date(current.lastBackupTime).toLocaleTimeString() : 'None'}
              subtitle={current.lastBackupTime ? new Date(current.lastBackupTime).toLocaleDateString() : undefined}
              color="text-pink-400"
            />
          </div>

          {current.pods && current.pods.length > 0 && (
            <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
              <div className="px-6 py-4 border-b border-border-primary">
                <h3 className="text-sm font-semibold text-text-primary">Pod Resources</h3>
              </div>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-text-tertiary border-b border-border-primary text-left">
                      <th className="px-6 py-3 font-medium">Pod</th>
                      <th className="px-6 py-3 font-medium">Role</th>
                      <th className="px-6 py-3 font-medium">CPU</th>
                      <th className="px-6 py-3 font-medium">Memory</th>
                    </tr>
                  </thead>
                  <tbody>
                    {current.pods.map((pod) => (
                      <tr key={pod.name} className="border-b border-border-primary last:border-0 hover:bg-surface-hover">
                        <td className="px-6 py-3 font-mono text-xs text-text-primary">{pod.name}</td>
                        <td className="px-6 py-3">
                          <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${pod.role === 'primary' ? 'bg-blue-500/20 text-blue-400' : 'bg-gray-500/20 text-gray-400'}`}>
                            {pod.role}
                          </span>
                        </td>
                        <td className="px-6 py-3 w-48">
                          <div className="flex items-center gap-2">
                            <span className="text-xs text-text-primary font-medium w-24">{pod.cpuCores.toFixed(3)} / {pod.cpuLimitCores}</span>
                          </div>
                          <UsageBar value={pod.cpuCores} max={pod.cpuLimitCores} color="bg-blue-500" />
                        </td>
                        <td className="px-6 py-3 w-48">
                          <div className="flex items-center gap-2">
                            <span className="text-xs text-text-primary font-medium w-24">{pod.memoryMB} / {pod.memoryLimitMB} MB</span>
                          </div>
                          <UsageBar value={pod.memoryMB} max={pod.memoryLimitMB} color="bg-purple-500" />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {chartData.length > 1 && chartData.some(d => d.cpu > 0 || d.memory > 0) && (
            <div className="bg-surface-card border border-border-primary rounded-xl p-6">
              <h3 className="text-sm font-semibold text-text-primary mb-6">Resource Usage Trends</h3>
              <ResponsiveContainer width="100%" height={280}>
                <AreaChart data={chartData}>
                  <defs>
                    <linearGradient id="gCpu" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%"  stopColor="#3b82f6" stopOpacity={0.5} />
                      <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="gMem" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%"  stopColor="#a855f7" stopOpacity={0.5} />
                      <stop offset="95%" stopColor="#a855f7" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--border-primary)" />
                  <XAxis dataKey="time" stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} />
                  <YAxis stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} unit="%" />
                  <Tooltip
                    contentStyle={{ backgroundColor: 'var(--surface-card)', border: '1px solid var(--border-primary)', borderRadius: '8px' }}
                    labelStyle={{ color: 'var(--text-primary)' }}
                    itemStyle={{ color: 'var(--text-secondary)' }}
                  />
                  <Legend />
                  <Area type="monotone" dataKey="cpu"    stroke="#3b82f6" fill="url(#gCpu)"  name="CPU %"    strokeWidth={2} />
                  <Area type="monotone" dataKey="memory" stroke="#a855f7" fill="url(#gMem)"  name="Memory %" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          )}
        </>
      )}
    </div>
  );
}
