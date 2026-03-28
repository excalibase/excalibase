import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useInstances } from '../hooks/useProvisioning';
import { useInstanceContext } from '../context/InstanceContext';
import { api } from '../api/client';
import { Bell, AlertTriangle, Info, XCircle } from 'lucide-react';

interface Alert {
  id: string;
  projectId: string;
  metricName: string;
  severity: 'WARNING' | 'CRITICAL';
  status: 'FIRING' | 'RESOLVED';
  message: string;
  currentValue: number;
  threshold: number;
  firedAt: string;
  resolvedAt?: string;
}

function useAlerts() {
  return useQuery({
    queryKey: ['alerts'],
    queryFn: async () => {
      const res = await api.get<Alert[]>('/alerts');
      return res.data;
    },
    refetchInterval: 30000,
  });
}

function useProjectAlerts(projectId: string) {
  return useQuery({
    queryKey: ['alerts', 'project', projectId],
    queryFn: async () => {
      const res = await api.get<Alert[]>(`/alerts/project/${projectId}`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function AlertsPage() {
  const { data: instances = [] } = useInstances();
  const { projectId: ctxProjectId } = useInstanceContext();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // null = not yet interacted → default to context selection; '' = user explicitly chose "All"
  const projectId = selectedId === null ? ctxProjectId : selectedId;

  const { data: allAlerts = [], isLoading: loadingAll } = useAlerts();
  const { data: projectAlerts = [] } = useProjectAlerts(projectId);

  const alerts = projectId ? projectAlerts : allAlerts;
  const isLoading = loadingAll;

  const severityIcon = (s: string) => {
    if (s === 'CRITICAL') return <XCircle className="w-4 h-4 text-color-error" />;
    if (s === 'WARNING')  return <AlertTriangle className="w-4 h-4 text-yellow-400" />;
    return <Info className="w-4 h-4 text-blue-400" />;
  };

  const severityBadge = (s: string) => {
    if (s === 'CRITICAL') return 'bg-red-900/30 text-color-error border-color-error/30';
    if (s === 'WARNING')  return 'bg-yellow-900/30 text-yellow-400 border-yellow-400/30';
    return 'bg-blue-900/30 text-blue-400 border-blue-400/30';
  };

  return (
    <div className="max-w-5xl mx-auto space-y-6">
      {/* Filter */}
      <div className="flex items-center gap-3">
        <label className="text-sm text-text-secondary font-medium">Filter by instance:</label>
        <select
          value={selectedId === null ? ctxProjectId : selectedId}
          onChange={(e) => setSelectedId(e.target.value)}
          className="px-3 py-2 bg-surface-card border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary"
        >
          <option value="">All instances</option>
          {instances.map((i) => (
            <option key={i.projectId} value={i.projectId}>{i.projectId}</option>
          ))}
        </select>
      </div>

      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        {isLoading ? (
          <div className="text-center py-16 text-text-secondary">Loading alerts...</div>
        ) : alerts.length === 0 ? (
          <div className="text-center py-16">
            <Bell className="w-12 h-12 mx-auto mb-3 text-text-tertiary" />
            <p className="text-text-secondary">No alerts.</p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
                <th className="text-left px-6 py-3 font-medium">Severity</th>
                <th className="text-left px-6 py-3 font-medium">Instance</th>
                <th className="text-left px-6 py-3 font-medium">Metric</th>
                <th className="text-left px-6 py-3 font-medium">Message</th>
                <th className="text-left px-6 py-3 font-medium">Fired At</th>
                <th className="text-left px-6 py-3 font-medium">State</th>
              </tr>
            </thead>
            <tbody>
              {alerts.map((a) => (
                <tr key={a.id} className="border-b border-border-primary last:border-0 hover:bg-surface-hover transition-colors">
                  <td className="px-6 py-4">
                    <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs border font-medium ${severityBadge(a.severity)}`}>
                      {severityIcon(a.severity)}
                      {a.severity}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-text-secondary font-medium">{a.projectId}</td>
                  <td className="px-6 py-4 text-text-tertiary text-xs font-mono">{a.metricName?.replace(/_/g, ' ')}</td>
                  <td className="px-6 py-4 text-text-primary">{a.message}</td>
                  <td className="px-6 py-4 text-text-tertiary">{a.firedAt ? new Date(a.firedAt).toLocaleString() : '—'}</td>
                  <td className="px-6 py-4">
                    <span className={`px-2 py-0.5 rounded text-xs border ${a.status === 'RESOLVED' ? 'bg-green-900/20 text-color-success border-color-success/30' : 'bg-red-900/10 text-color-error border-color-error/30'}`}>
                      {a.status === 'RESOLVED' ? 'Resolved' : 'Firing'}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
