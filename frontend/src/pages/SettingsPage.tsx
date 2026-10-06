import { Link, useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Loader2, Server, Database, Shield, Clock, Trash2, Check, AlertTriangle, PauseCircle, PlayCircle } from 'lucide-react';
import { useState } from 'react';
import { api } from '../api/client';
import { useCancelDeletion, useDeprovisionDatabase, usePauseProject, useResumeProject, useSetDeletionProtection } from '../hooks/useProvisioning';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import { ConnectionStrings } from '../components/ConnectionStrings';
import { ConnectSnippets } from '../components/ConnectSnippets';
import { MinorUpgradeCard } from '../components/MinorUpgradeCard';
import { ClusterSettingsCard } from '../components/ClusterSettingsCard';
import { PublicPortCard } from '../components/PublicPortCard';
import { AppNetworkSection } from '../components/AppNetworkCard';
import { useProjectEndpoint } from '../api/projectEndpoint';
import type { DatabaseInstance } from '../types';
import { DELETION_PROTECTED_REASON, isDeletionProtected } from '../utils/deletionProtection';
import { serverErrorMessage } from '../utils/serverError';
import { engineLabel } from '../utils/engine';
import { projectOperationMessage } from '../hooks/projectFollow';

interface RollbackResult {
  name: string;
  ok: boolean;
  error?: string;
}

function projectInfo(project: DatabaseInstance) {
  return [
    { icon: Server, label: 'Display Name', value: project.projectName || '-' },
    { icon: Database, label: 'Database Type', value: engineLabel(project) },
    { icon: Shield, label: 'Tier', value: project.tier },
    { icon: Database, label: 'PostgreSQL Version', value: project.postgresVersion || '-' },
    { icon: Server, label: 'Namespace', value: project.namespace },
    { icon: Server, label: 'Host', value: project.host || '-' },
    { icon: Server, label: 'Port', value: project.port || '-' },
    { icon: Database, label: 'Database Name', value: project.databaseName || '-' },
    { icon: Clock, label: 'Created', value: project.createdAt ? new Date(project.createdAt).toLocaleString() : '-' },
    { icon: Clock, label: 'Updated', value: project.updatedAt ? new Date(project.updatedAt).toLocaleString() : '-' },
  ];
}

export function SettingsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [showDelete, setShowDelete] = useState(false);
  const deprovision = useDeprovisionDatabase();
  const { data: project, isLoading, error: loadError } = useQuery({
    queryKey: ['project', projectId],
    queryFn: async () => {
      const res = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return res.data;
    },
    enabled: !!projectId,
  });
  // The public host, port, TLS posture and cluster CA all come from the
  // control plane. A failed read leaves this undefined, and the connection
  // card falls back to the in-cluster details rather than guessing. A project
  // without a database has no endpoint to read.
  const endpoint = useProjectEndpoint(project && !project.noDatabase ? projectId : undefined);

  if (loadError && !project) {
    return (
      <p data-testid="settings-load-error" role="alert" className="py-16 text-center text-sm text-red-400">
        {serverErrorMessage(loadError, 'The project could not be loaded')}
      </p>
    );
  }
  if (isLoading || !project) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  // A major that can carry DocumentDB says nothing about whether this project
  // was created with it; only the project record does.
  const documentDb = project.documentDb === true;
  const protectedFromDeletion = isDeletionProtected(project);

  const info = projectInfo(project);
  const noDatabase = project.noDatabase === true;


  return (
    <div data-testid="settings-page">
      <h3 className="text-lg font-semibold text-text-primary mb-4">Project Settings</h3>

      {project.status === 'FAILED' && <ProvisionFailedBanner project={project} />}

      {noDatabase && (
        <div className="rounded-lg border border-border-primary bg-surface-card p-4 mb-8" data-testid="settings-no-database">
          <p className="text-sm text-text-secondary">
            This project has no database, so it has no connection details, public port, backups or database
            settings.{' '}
            {project.canAddDatabase && (
              <Link to={`/project/${project.projectId}/database/add`} className="text-purple-400 hover:underline">
                Add a database
              </Link>
            )}
          </p>
        </div>
      )}

      {!noDatabase && (<>
      <div className="rounded-lg border border-border-primary bg-surface-card p-4 mb-8" data-testid="connect-section">
        <h4 className="text-sm font-medium text-text-primary mb-3">Connect to your project</h4>
        <ConnectSnippets projectId={project.projectId} />
      </div>

      <div className="mb-8">
        <ConnectionStrings projectId={project.projectId} documentDb={documentDb} endpoint={endpoint.data} />
      </div>

      <div className="mb-8">
        <PublicPortCard projectId={project.projectId} status={project.status} />
      </div>
      </>)}

      <AppNetworkSection projectId={project.projectId} status={project.status} />

      <div className="rounded-lg border border-border-primary bg-surface-card overflow-hidden mb-8">
        {info.map(({ icon: Icon, label, value }) => (
          <div key={label} className="flex items-center gap-3 px-4 py-3 border-b border-border-primary last:border-0">
            <Icon className="w-4 h-4 text-text-tertiary flex-shrink-0" />
            <span className="text-sm text-text-secondary w-36">{label}</span>
            <span className="text-sm text-text-primary font-medium">{value}</span>
          </div>
        ))}
      </div>

      {!noDatabase && (<>
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

      <div className="mb-8">
        <MinorUpgradeCard project={project} />
      </div>

      <div className="mb-8">
        <ClusterSettingsCard project={project} />
      </div>

      <LifecycleSection project={project} />
      </>)}

      <DangerZone
        project={project}
        protectedFromDeletion={protectedFromDeletion}
        onDelete={() => setShowDelete(true)}
        deleting={deprovision.isPending}
        deleteError={deprovision.error}
      />

      <ConfirmModal
        open={showDelete}
        onClose={() => setShowDelete(false)}
        onConfirm={() => {
          if (projectId) deprovision.mutate(projectId, {
            onSuccess: () => navigate('/instances'),
            onError: () => setShowDelete(false),
          });
        }}
        title="Delete Project"
        message={`This stops "${projectId}" now and permanently deletes it and all associated data after 7 days. An org owner can cancel until then.`}
        confirmText={projectId}
        confirmLabel="Delete Project"
        destructive
        loading={deprovision.isPending}
      />
    </div>
  );
}

// Pause and resume; the pre-pause backup runs on the server.
function LifecycleSection({ project }: { readonly project: DatabaseInstance }) {
  const pauseProject = usePauseProject();
  const resumeProject = useResumeProject();
  const failure = pauseProject.error ?? resumeProject.error;
  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="lifecycle-section">
      <h4 className="text-sm font-medium text-text-primary mb-2">Lifecycle</h4>
      {(pauseProject.isPending || resumeProject.isPending) && (
        <p className="text-xs text-text-secondary mb-3" data-testid="lifecycle-pending" role="status">
          {pauseProject.isPending
            ? 'Pausing: taking a backup, then stopping the database. This can take a few minutes.'
            : 'Resuming: starting the database. This can take a few minutes.'}
        </p>
      )}
      {failure && (
        <p className="text-xs text-red-400 mb-3 break-words" data-testid="lifecycle-error" role="alert">
          {projectOperationMessage(failure, 'The project could not be changed')}
        </p>
      )}
      {project.status === 'PAUSED' ? (
        <>
          <p className="text-xs text-text-secondary mb-3">
            This project is paused {project.pauseReason ? `(${project.pauseReason})` : ''}. Click Resume to bring it back online.
            {project.lastActiveAt && (
              <> Last active: {new Date(project.lastActiveAt).toLocaleString()}.</>
            )}
          </p>
          <button
            onClick={() => resumeProject.mutate(project.projectId)}
            disabled={resumeProject.isPending}
            className="flex items-center gap-2 px-4 py-2 bg-green-500 hover:bg-green-600 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50"
            data-testid="resume-project-btn"
          >
            {resumeProject.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <PlayCircle className="w-4 h-4" />}
            Resume Project
          </button>
        </>
      ) : (
        <>
          <p className="text-xs text-text-secondary mb-3">
            Pause stops the database workload after taking a backup. Data persists; you can Resume anytime.
          </p>
          <button
            onClick={() => pauseProject.mutate({ projectId: project.projectId })}
            disabled={pauseProject.isPending || project.status !== 'ACTIVE'}
            className="flex items-center gap-2 px-4 py-2 bg-amber-500 hover:bg-amber-600 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50"
            data-testid="pause-project-btn"
          >
            {pauseProject.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <PauseCircle className="w-4 h-4" />}
            Pause Project
          </button>
        </>
      )}
    </div>
  );
}

interface DangerZoneProps {
  readonly project: DatabaseInstance;
  readonly protectedFromDeletion: boolean;
  readonly onDelete: () => void;
  readonly deleting: boolean;
  readonly deleteError: unknown;
}

// Deletion protection, deletion and its 7-day grace.
function DangerZone({ project, protectedFromDeletion, onDelete, deleting, deleteError }: DangerZoneProps) {
  const setProtection = useSetDeletionProtection();
  const cancelDeletion = useCancelDeletion();
  return (
    <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-4">
      <h4 className="text-sm font-medium text-red-400 mb-2">Danger Zone</h4>
      {deleting && (
        <p className="text-xs text-text-secondary mb-3" data-testid="delete-pending" role="status">
          Stopping the project for deletion: taking a backup, then stopping the database. This can take a few minutes.
        </p>
      )}
      {deleteError != null && !deleting && (
        <p className="text-xs text-red-400 mb-3 break-words" data-testid="delete-error" role="alert">
          The project was not scheduled for deletion: {projectOperationMessage(deleteError, 'the request was refused')}
        </p>
      )}
      {project.status === 'PENDING_DELETION' ? (
        <div data-testid="deletion-scheduled">
          <p className="text-xs text-text-secondary mb-3">
            This project is scheduled for deletion on{' '}
            {project.deletionDueAt ? new Date(project.deletionDueAt).toLocaleString() : 'its due date'}.{' '}
            {project.noDatabase === true
              ? 'Its files and containers are kept until then. An org owner can cancel; the project then carries on as before.'
              : 'Its database is stopped and its data kept until then. An org owner can cancel; the project is then left paused.'}
          </p>
          {cancelDeletion.error != null && (
            <p className="text-xs text-red-400 mb-3 break-words" data-testid="cancel-deletion-error" role="alert">
              {projectOperationMessage(cancelDeletion.error, 'The deletion could not be cancelled; retry the request.')}
            </p>
          )}
          <button
            onClick={() => cancelDeletion.mutate(project.projectId)}
            disabled={cancelDeletion.isPending}
            className="flex items-center gap-2 px-4 py-2 border border-red-500/40 text-red-400 hover:bg-red-500/10 text-sm font-medium rounded-lg transition-colors disabled:opacity-50"
            data-testid="cancel-deletion-btn"
          >
            <Shield className="w-4 h-4" /> Cancel deletion
          </button>
        </div>
      ) : (
        <>
          <p className="text-xs text-text-secondary mb-3" data-testid="deletion-grace-note">
            {project.noDatabase === true
              ? 'Deleting schedules this project for permanent removal, with its files and containers, 7 days later. Until then an org owner can cancel.'
              : 'Deleting stops this project now and permanently removes its data 7 days later. Until then an org owner can cancel. Kept backups are purged 14 days after that.'}
          </p>
          <p className="text-xs text-text-secondary mb-3" data-testid="deletion-protection-state">
            {protectedFromDeletion
              ? DELETION_PROTECTED_REASON
              : 'Deletion protection is off. Any org admin can delete this project.'}
          </p>
          {setProtection.error != null && (
            <p className="text-xs text-red-400 mb-3 break-words" data-testid="deletion-protection-error" role="alert">
              {serverErrorMessage(setProtection.error, 'Deletion protection was not changed')}
            </p>
          )}
          <div className="flex gap-2">
            <button
              onClick={() => setProtection.mutate({ projectId: project.projectId, enabled: !protectedFromDeletion })}
              disabled={setProtection.isPending}
              className="flex items-center gap-2 px-4 py-2 border border-red-500/40 text-red-400 hover:bg-red-500/10 text-sm font-medium rounded-lg transition-colors disabled:opacity-50"
              data-testid="deletion-protection-btn"
            >
              <Shield className="w-4 h-4" />
              {protectedFromDeletion ? 'Turn off deletion protection' : 'Turn on deletion protection'}
            </button>
            <button
              onClick={onDelete}
              disabled={protectedFromDeletion}
              className="flex items-center gap-2 px-4 py-2 bg-red-500 hover:bg-red-600 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              data-testid="delete-project-btn"
            >
              <Trash2 className="w-4 h-4" /> Delete Project
            </button>
          </div>
        </>
      )}
    </div>
  );
}

// parseRollbackLog safely decodes the JSON rollback log; a malformed/absent log
// renders no rollback section rather than throwing.
function parseRollbackLog(log?: string): RollbackResult[] {
  if (!log) return [];
  try {
    return JSON.parse(log) as RollbackResult[];
  } catch {
    return [];
  }
}

// ProvisionFailedBanner renders the failure stage/reason and optional rollback
// log for a FAILED project. Extracted from SettingsPage to keep that component
// flat (the nested failure/rollback conditionals dominated its complexity).
function ProvisionFailedBanner({ project }: { readonly project: DatabaseInstance }) {
  const rollbackResults = parseRollbackLog(project.rollbackLog);
  return (
    <div className="rounded-lg border border-red-500/40 bg-red-500/5 p-4 mb-6">
      <div className="flex items-start gap-3">
        <AlertTriangle className="w-5 h-5 text-red-400 flex-shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium text-red-400 mb-1">
            Provisioning failed at stage {project.failureStage ?? project.currentStage}
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
                  <li key={`${r.name}-${i}`} className="text-xs font-mono flex items-center gap-2">
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
  );
}
