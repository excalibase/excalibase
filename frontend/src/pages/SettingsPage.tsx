import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Loader2, Server, Database, Shield, Clock, Trash2, Copy, Check, AlertTriangle } from 'lucide-react';
import { useState } from 'react';
import { api } from '../api/client';
import { useDeprovisionDatabase } from '../hooks/useProvisioning';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import type { DatabaseInstance } from '../types';

interface RollbackResult {
  name: string;
  ok: boolean;
  error?: string;
}

function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = () => {
    navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <div className="flex items-center gap-2">
        <code className="flex-1 bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-sm font-mono text-text-primary truncate">
          {value}
        </code>
        <button
          type="button"
          onClick={onCopy}
          aria-label={`Copy ${label}`}
          className="p-2 rounded border border-border-primary text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
        >
          {copied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
    </div>
  );
}

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
    { icon: Server, label: 'Display Name', value: project.projectName || '-' },
    { icon: Database, label: 'Database Type', value: project.databaseType },
    { icon: Shield, label: 'Tier', value: project.tier },
    { icon: Server, label: 'Namespace', value: project.namespace },
    { icon: Server, label: 'Host', value: project.host || '-' },
    { icon: Server, label: 'Port', value: project.port || '-' },
    { icon: Database, label: 'Database Name', value: project.databaseName || '-' },
    { icon: Clock, label: 'Created', value: project.createdAt ? new Date(project.createdAt).toLocaleString() : '-' },
    { icon: Clock, label: 'Updated', value: project.updatedAt ? new Date(project.updatedAt).toLocaleString() : '-' },
  ];

  // Connection endpoints — these come from env vars in the real deployment, but here
  // we build them from the project ref so users have something to copy immediately.
  const authEndpoint = `https://auth.excalibase.io/${project.orgId}/${project.projectId}`;
  const graphqlEndpoint = `https://api.excalibase.io/${project.orgId}/${project.projectId}/graphql`;
  const sdkSnippet = `import { createClient } from '@excalibase/client'

const excalibase = createClient({
  url: 'https://api.excalibase.io/${project.orgId}/${project.projectId}',
  anonKey: '<paste your anon key from Auth settings>',
})`;

  let rollbackResults: RollbackResult[] = [];
  if (project.rollbackLog) {
    try {
      rollbackResults = JSON.parse(project.rollbackLog) as RollbackResult[];
    } catch {
      // ignore parse error — just don't render rollback section
    }
  }

  return (
    <div data-testid="settings-page">
      <h3 className="text-lg font-semibold text-text-primary mb-4">Project Settings</h3>

      {/* Failure banner */}
      {project.status === 'FAILED' && (
        <div className="rounded-lg border border-red-500/40 bg-red-500/5 p-4 mb-6">
          <div className="flex items-start gap-3">
            <AlertTriangle className="w-5 h-5 text-red-400 flex-shrink-0 mt-0.5" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400 mb-1">
                Provisioning failed at stage {project.failureStage || project.currentStage}
                {project.failureStep ? ` (${project.failureStep})` : ''}
              </div>
              {project.failureReason && (
                <div className="text-xs text-text-secondary font-mono break-words">
                  {project.failureReason}
                </div>
              )}
              {rollbackResults.length > 0 && (
                <details className="mt-3">
                  <summary className="text-xs text-text-tertiary cursor-pointer hover:text-text-secondary">
                    Rollback log ({rollbackResults.length} action{rollbackResults.length === 1 ? '' : 's'})
                  </summary>
                  <ul className="mt-2 space-y-1">
                    {rollbackResults.map((r, i) => (
                      <li key={i} className="text-xs font-mono flex items-center gap-2">
                        {r.ok ? (
                          <Check className="w-3 h-3 text-green-400 flex-shrink-0" />
                        ) : (
                          <AlertTriangle className="w-3 h-3 text-red-400 flex-shrink-0" />
                        )}
                        <span className={r.ok ? 'text-text-secondary' : 'text-red-400'}>
                          {r.name}{r.error ? `: ${r.error}` : ''}
                        </span>
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Connect section (Supabase-style) */}
      <div className="rounded-lg border border-border-primary bg-surface-card p-4 mb-8" data-testid="connect-section">
        <h4 className="text-sm font-medium text-text-primary mb-3">Connect to your project</h4>
        <div className="space-y-3">
          <CopyField label="Project Ref" value={project.projectId} />
          <CopyField label="GraphQL endpoint" value={graphqlEndpoint} />
          <CopyField label="Auth endpoint" value={authEndpoint} />
          <div>
            <div className="text-xs text-text-tertiary mb-1">SDK init</div>
            <pre className="bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary overflow-x-auto">
              <code>{sdkSnippet}</code>
            </pre>
          </div>
        </div>
      </div>

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
