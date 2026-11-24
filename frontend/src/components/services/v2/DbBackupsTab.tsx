import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { useListBackups, useTriggerBackup } from '../../../hooks/useProvisioning';
import { useProjectRole } from '../../../hooks/useProjectRole';
import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { backupView } from '../../../utils/backupView';
import { describeBackupSchedule } from '../../../utils/backupSchedule';
import { formatBytes } from '../../../utils/formatBytes';
import { isMySQLProject } from '../../../utils/engine';
import { serverErrorMessage } from '../../../utils/serverError';
import type { BackupInfo, DatabaseInstance } from '../../../types';
import { StatusBadge } from '../../shared/StatusBadge';
import { RestoreForm } from '../../backups/RestoreForm';
import { CopyDatabaseForm } from '../../backups/CopyDatabaseForm';
import { formatWhen } from '../../containers/appCopy';
import { Spinner, secondaryButton } from '../../containers/ContainerBits';
import { Fact, Notice, Section } from './sections';

function BackupRow({ backup }: { readonly backup: BackupInfo }) {
  return (
    <li data-testid={`backup-row-${backup.id}`} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2.5">
      <div className="min-w-0">
        <p className="break-all font-mono text-xs text-text-primary">{backup.id}</p>
        <p className="text-xs text-text-tertiary">
          {formatWhen(backup.timestamp)} · {backup.type ?? 'MANUAL'} · {formatBytes(backup.sizeBytes ?? 0)}
        </p>
      </div>
      <StatusBadge status={backup.status} />
    </li>
  );
}

function PausedNote({ newest, isPauseBackup }: { readonly newest?: BackupInfo; readonly isPauseBackup: boolean }) {
  const detail = newest ? `${isPauseBackup ? '; the pause took a final backup' : '; the latest completed backup is'} ${newest.id}` : '';
  return <Notice testId="paused-backups">Paused: nightly backups stop while paused{detail}</Notice>;
}

function BackupHistory({ project }: { readonly project: DatabaseInstance }) {
  const queryClient = useQueryClient();
  const { data, isLoading } = useListBackups(project.projectId);
  const trigger = useTriggerBackup();
  const [started, setStarted] = useState(false);
  const paused = project.status === 'PAUSED';
  const { backups, newestCompleted, isPauseBackup } = backupView(data?.backups, paused, project.pauseBackupId);
  const newestFirst = [...backups].sort((a, b) => b.timestamp.localeCompare(a.timestamp));
  const run = () => {
    setStarted(false);
    trigger.mutate(project.projectId, {
      onSuccess: () => {
        setStarted(true);
        queryClient.invalidateQueries({ queryKey: ['backups', project.projectId] });
      },
    });
  };
  return (
    <>
      {paused && <PausedNote newest={newestCompleted} isPauseBackup={isPauseBackup} />}
      <Section
        id="db-backup-history"
        title="Backups"
        aside={
          <button type="button" onClick={run} disabled={trigger.isPending} className={secondaryButton}>
            <RefreshCw className="h-4 w-4" aria-hidden="true" /> Back up now
          </button>
        }
      >
        {started && <output className="block text-xs text-green-400">Backup started</output>}
        {trigger.isError && <p role="alert" className="break-words text-xs text-red-400">{serverErrorMessage(trigger.error, 'The backup was not started')}</p>}
        {data && (
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            <Fact label="Backups">{data.backupEnabled ? 'Enabled' : 'Disabled'}</Fact>
            <Fact label="Schedule">{describeBackupSchedule(data.schedule)}</Fact>
            <Fact label="Retention">{data.retentionDays == null ? '—' : `${data.retentionDays} days`}</Fact>
          </dl>
        )}
        {isLoading && <Spinner />}
        {!isLoading && newestFirst.length === 0 && <p className="text-sm text-text-secondary">No backups yet. Use Back up now to take the first one.</p>}
        {newestFirst.length > 0 && <ul className="divide-y divide-border-primary">{newestFirst.map((b) => <BackupRow key={b.id} backup={b} />)}</ul>}
      </Section>
    </>
  );
}

function RestoreSections({ project }: { readonly project: DatabaseInstance }) {
  const { data } = useListBackups(project.projectId);
  const { enabled: copyOn } = useFeatureEnabled('dbcopy');
  const mysql = isMySQLProject(project);
  const engine = mysql ? 'mysql' : undefined;
  const restoreWindow = mysql ? data?.restoreWindow : undefined;
  return (
    <>
      <Section id="db-restore" title={mysql ? 'Restore' : 'Restore or point-in-time restore'}>
        <RestoreForm projectId={project.projectId} engine={engine} restoreWindow={restoreWindow} />
      </Section>
      {copyOn && (
        <Section id="db-copy" title="Copy into a new project">
          <CopyDatabaseForm projectId={project.projectId} engine={engine} restoreWindow={restoreWindow} />
        </Section>
      )}
    </>
  );
}

// Backups, restore and copy. The server asks for an admin on every one of
// these calls, so anyone below that sees the reason and none of the controls.
export function DbBackupsTab({ project }: { readonly project: DatabaseInstance }) {
  const { canAdmin, isLoading } = useProjectRole(project.projectId);
  if (isLoading) return <Spinner />;
  if (!canAdmin) return <Notice>Only an admin of this organization can back up or restore this database. Ask an admin.</Notice>;
  return (
    <div className="space-y-5" data-testid="db-backups">
      <BackupHistory project={project} />
      <RestoreSections project={project} />
    </div>
  );
}
