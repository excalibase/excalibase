import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Loader2, Server, Database, Shield, Clock, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { api } from '../api/client';
import { useDeprovisionDatabase } from '../hooks/useProvisioning';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import type { DatabaseInstance } from '../types';

export function SettingsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [showDelete, setShowDelete] = useState(false);
  const deprovision = useDeprovisionDatabase();

  const { data: project, isLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: async () => {
      const res = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return res.data;
    },
    enabled: !!projectId,
  });

  if (isLoading || !project) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  const info = [
    { icon: Server, label: 'Project ID', value: project.projectId },
    { icon: Database, label: 'Database Type', value: project.databaseType },
    { icon: Shield, label: 'Tier', value: project.tier },
    { icon: Server, label: 'Namespace', value: project.namespace },
    { icon: Server, label: 'Host', value: project.host || '-' },
    { icon: Server, label: 'Port', value: project.port || '-' },
    { icon: Database, label: 'Database Name', value: project.databaseName || '-' },
    { icon: Clock, label: 'Created', value: project.createdAt ? new Date(project.createdAt).toLocaleString() : '-' },
    { icon: Clock, label: 'Updated', value: project.updatedAt ? new Date(project.updatedAt).toLocaleString() : '-' },
  ];

  return (
    <div data-testid="settings-page">
      <h3 className="text-lg font-semibold text-text-primary mb-4">Project Settings</h3>

      {/* Info grid */}
      <div className="rounded-lg border border-border-primary bg-surface-card overflow-hidden mb-8">
        {info.map(({ icon: Icon, label, value }) => (
          <div key={label} className="flex items-center gap-3 px-4 py-3 border-b border-border-primary last:border-0">
            <Icon className="w-4 h-4 text-text-tertiary flex-shrink-0" />
            <span className="text-sm text-text-secondary w-36">{label}</span>
            <span className="text-sm text-text-primary font-medium">{value}</span>
          </div>
        ))}
      </div>

      {/* Backup info */}
      <div className="rounded-lg border border-border-primary bg-surface-card p-4 mb-8">
        <h4 className="text-sm font-medium text-text-primary mb-2">Backup Configuration</h4>
        <div className="grid grid-cols-3 gap-4 text-sm">
          <div>
            <span className="text-text-tertiary block text-xs">Enabled</span>
            <span className="text-text-primary">{project.backupEnabled ? 'Yes' : 'No'}</span>
          </div>
          <div>
            <span className="text-text-tertiary block text-xs">Schedule</span>
            <span className="text-text-primary font-mono text-xs">{project.backupSchedule || '-'}</span>
          </div>
          <div>
            <span className="text-text-tertiary block text-xs">Retention</span>
            <span className="text-text-primary">{project.backupRetentionDays ? `${project.backupRetentionDays} days` : '-'}</span>
          </div>
        </div>
      </div>

      {/* Danger zone */}
      <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-medium text-red-400 mb-2">Danger Zone</h4>
        <p className="text-xs text-text-secondary mb-3">
          Deleting this project will permanently remove all data, backups, and configurations.
        </p>
        <button
          onClick={() => setShowDelete(true)}
          className="flex items-center gap-2 px-4 py-2 bg-red-500 hover:bg-red-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="delete-project-btn"
        >
          <Trash2 className="w-4 h-4" /> Delete Project
        </button>
      </div>

      <ConfirmModal
        open={showDelete}
        onClose={() => setShowDelete(false)}
        onConfirm={() => {
          if (projectId) deprovision.mutate(projectId, {
            onSuccess: () => { window.location.href = '/projects'; },
          });
        }}
        title="Delete Project"
        message={`This will permanently delete "${projectId}" and all associated data. This action cannot be undone.`}
        confirmText={projectId}
        confirmLabel="Delete Project"
        destructive
        loading={deprovision.isPending}
      />
    </div>
  );
}
