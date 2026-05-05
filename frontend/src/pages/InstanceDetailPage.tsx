import { useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useInstance, useDeprovisionDatabase, useListBackups, useTriggerBackup, useLogs } from '../hooks/useProvisioning';
import { useCurrentMetrics } from '../hooks/useMetrics';
import { StageTimeline } from '../components/shared/StageTimeline';
import { StatusBadge } from '../components/shared/StatusBadge';
import { MetricCard } from '../components/shared/MetricCard';
import { CredentialsViewer } from '../components/CredentialsViewer';
import { Button } from '../components/Button';
import { ArrowLeft, Cpu, Database, HardDrive, Users, Loader2, Trash2, RefreshCw, Archive, FileText } from 'lucide-react';

type Tab = 'overview' | 'credentials' | 'backups' | 'logs';

export function InstanceDetailPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [tab, setTab] = useState<Tab>('overview');

  const { data: instance, isLoading } = useInstance(projectId!);
  const { data: metrics } = useCurrentMetrics(projectId!);
  const { data: backupData } = useListBackups(projectId!);
  const backups = Array.isArray(backupData?.backups) ? backupData.backups : [];
  const { data: logs } = useLogs(projectId!, 200);
  const deprovision = useDeprovisionDatabase();
  const triggerBackup = useTriggerBackup();

  const handleDelete = async () => {
    if (confirm(`Delete ${projectId}? This cannot be undone.`)) {
      await deprovision.mutateAsync(projectId!);
      navigate('/instances');
    }
  };

  if (isLoading || !instance) {
    return (
      <div className="flex items-center justify-center py-24">
        <Loader2 className="w-8 h-8 animate-spin text-accent-primary" />
      </div>
    );
  }

  return (
    <div className="max-w-6xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-3">
          <button
            onClick={() => navigate('/instances')}
            className="p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
          >
            <ArrowLeft className="w-5 h-5" />
          </button>
          <div>
            <div className="flex items-center gap-3">
              <h2 className="text-xl font-bold text-text-primary">{instance.projectId}</h2>
              <StatusBadge stage={instance.currentStage} />
            </div>
            <p className="text-sm text-text-tertiary mt-0.5">
              {instance.databaseType} · {instance.tier} · {instance.namespace}
            </p>
          </div>
        </div>
        <Button variant="danger" size="sm" onClick={handleDelete} disabled={deprovision.isPending}>
          <Trash2 className="w-4 h-4 mr-1.5" />
          Deprovision
        </Button>
      </div>

      {/* Stage timeline */}
      <div className="bg-surface-card border border-border-primary rounded-xl p-6">
        <h3 className="text-sm font-semibold text-text-primary mb-5">Provisioning Pipeline</h3>
        <StageTimeline currentStage={instance.currentStage} failureReason={instance.failureReason} />
      </div>

      {/* Live metrics. Metric fields are nullable while the project is
          spinning up or scaling — render "—" rather than NaN. */}
      {metrics && (
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <MetricCard icon={<Cpu className="w-4 h-4" />}      label="CPU"         value={metrics.cpuUsagePercent == null ? '—' : `${Math.round(metrics.cpuUsagePercent)}%`}    subtitle={metrics.cpuUsageCores == null ? '—' : `${metrics.cpuUsageCores.toFixed(1)} cores`}  color="text-blue-400" />
          <MetricCard icon={<Database className="w-4 h-4" />}  label="Memory"      value={metrics.memoryUsagePercent == null ? '—' : `${Math.round(metrics.memoryUsagePercent)}%`} subtitle={metrics.memoryUsageMB == null ? '—' : `${metrics.memoryUsageMB} MB`}                color="text-purple-400" />
          <MetricCard icon={<HardDrive className="w-4 h-4" />} label="Disk"        value={metrics.diskUsagePercent == null ? '—' : `${Math.round(metrics.diskUsagePercent)}%`}   subtitle={metrics.diskUsageGB == null ? '—' : `${metrics.diskUsageGB} GB`}                  color="text-orange-400" />
          <MetricCard icon={<Users className="w-4 h-4" />}     label="Connections" value={metrics.activeConnections ?? '—'}                                                       subtitle={`/ ${metrics.maxConnections ?? '—'} max`}                                            color="text-green-400" />
        </div>
      )}

      {/* Tabs */}
      <div className="bg-surface-card border border-border-primary rounded-xl">
        <div className="flex border-b border-border-primary px-4">
          {(['overview', 'credentials', 'backups', 'logs'] as Tab[]).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-4 py-3 text-sm font-medium capitalize border-b-2 -mb-px transition-colors ${
                tab === t
                  ? 'border-accent-primary text-accent-primary'
                  : 'border-transparent text-text-secondary hover:text-text-primary'
              }`}
            >
              {t}
            </button>
          ))}
        </div>

        <div className="p-6">
          {tab === 'overview' && (
            <div className="grid grid-cols-2 md:grid-cols-3 gap-6 text-sm">
              {[
                ['Organization',  instance.orgId],
                ['Host',          instance.host ?? '—'],
                ['Port',          instance.port ?? '—'],
                ['Database Name', instance.databaseName ?? '—'],
                ['Backup',        instance.backupEnabled ? `Enabled · ${instance.backupSchedule}` : 'Disabled'],
                ['Created',       new Date(instance.createdAt).toLocaleString()],
                ['Updated',       new Date(instance.updatedAt).toLocaleString()],
                ['Metrics',       instance.metricsEndpoint ?? '—'],
              ].map(([label, value]) => (
                <div key={label as string}>
                  <p className="text-text-tertiary mb-1">{label}</p>
                  <p className="text-text-primary font-medium break-all">{value}</p>
                </div>
              ))}
            </div>
          )}

          {tab === 'credentials' && (
            <CredentialsViewer projectId={projectId!} />
          )}

          {tab === 'logs' && (
            <div>
              {logs == null && (
                <div className="text-center py-10 text-text-secondary text-sm">Loading logs…</div>
              )}
              {logs?.trim() === '' && (
                <div className="text-center py-10 text-text-secondary text-sm">
                  <FileText className="w-10 h-10 mx-auto mb-2 text-text-tertiary" />
                  No logs available (pod may not be running in this environment).
                </div>
              )}
              {logs != null && logs.trim() !== '' && (
                <pre className="bg-bg-primary rounded-lg p-4 text-xs font-mono text-text-secondary overflow-auto max-h-[500px] whitespace-pre-wrap break-all">
                  {logs}
                </pre>
              )}
            </div>
          )}

          {tab === 'backups' && (
            <div className="space-y-4">
              <div className="flex justify-end">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={triggerBackup.isPending}
                  onClick={() => triggerBackup.mutate(projectId!)}
                >
                  <RefreshCw className="w-4 h-4 mr-1.5" />
                  Trigger Backup
                </Button>
              </div>
              {backups.length === 0 ? (
                <div className="text-center py-10">
                  <Archive className="w-10 h-10 mx-auto mb-2 text-text-tertiary" />
                  <p className="text-text-secondary text-sm">No backups yet.</p>
                </div>
              ) : (
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-text-tertiary border-b border-border-primary">
                      <th className="text-left py-2 font-medium">ID</th>
                      <th className="text-left py-2 font-medium">Timestamp</th>
                      <th className="text-left py-2 font-medium">Size</th>
                      <th className="text-left py-2 font-medium">Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {backups.map((b) => (
                      <tr key={b.id} className="border-b border-border-primary last:border-0">
                        <td className="py-3 font-mono text-xs text-text-primary">{b.id}</td>
                        <td className="py-3 text-text-secondary">{new Date(b.timestamp).toLocaleString()}</td>
                        <td className="py-3 text-text-secondary">{b.size}</td>
                        <td className="py-3"><StatusBadge status={b.status} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
