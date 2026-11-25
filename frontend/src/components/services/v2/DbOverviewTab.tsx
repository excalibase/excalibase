import { useState } from 'react';
import { ProvisionFailure } from '../../shared/ProvisionFailure';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Loader2, PauseCircle, PlayCircle } from 'lucide-react';
import { useCurrentMetrics } from '../../../hooks/useMetrics';
import { useProjectRole } from '../../../hooks/useProjectRole';
import { useDeprovisionDatabase, useListBackups, usePauseProject, useResumeProject } from '../../../hooks/useProvisioning';
import { projectOperationMessage } from '../../../hooks/projectFollow';
import { useTiers } from '../../../api/tiers';
import type { BackupInfo, DatabaseInstance } from '../../../types';
import { DangerZone } from '../../../pages/SettingsPage';
import { isDeletionProtected } from '../../../utils/deletionProtection';
import { databaseSize } from '../../../utils/databaseSize';
import { ConfirmModal } from '../../ui/ConfirmModal';
import { formatWhen } from '../../containers/appCopy';
import { ToneBadge } from '../../containers/ContainerBits';
import { databaseServiceStatus } from '../serviceStatus';
import { panelSearch } from './panelState';
import { card, Fact } from './sections';
import { parseInstant } from '../../../utils/instant';

const NOT_AVAILABLE = 'Not available';

function LiveState({ project }: { readonly project: DatabaseInstance }) {
  const { data: metrics, dataUpdatedAt } = useCurrentMetrics(project.projectId);
  const { data: backupList } = useListBackups(project.projectId);
  const { data: tiers } = useTiers();
  const tier = tiers?.find((t) => t.tier === project.tier);
  const status = databaseServiceStatus(project);
  const size = metrics ? databaseSize(metrics) : undefined;
  const disk = metrics?.diskUsageGB == null ? undefined : `${metrics.diskUsageGB} GB`;
  const compute = tier ? `${tier.cpu} vCPU · ${tier.memory}` : NOT_AVAILABLE;
  const newest = latestBackup(backupList?.backups);
  return (
    <section aria-labelledby="db-live-state" className={card}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 id="db-live-state" className="text-sm font-semibold text-text-primary">Live state</h3>
        {dataUpdatedAt > 0 && <p className="text-xs text-text-tertiary">Read from the running database, checked {new Date(parseInstant(dataUpdatedAt)).toLocaleTimeString()}</p>}
      </div>
      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Fact label="Status"><ToneBadge {...status} /></Fact>
        <Fact label="Version">{project.postgresVersion ?? project.mysqlVersion ?? NOT_AVAILABLE}</Fact>
        <Fact label="Size">{size ?? disk ?? NOT_AVAILABLE}</Fact>
        <Fact label="Connections">{metrics?.activeConnections == null ? NOT_AVAILABLE : `${metrics.activeConnections} of ${metrics.maxConnections ?? '—'}`}</Fact>
        <Fact label="Last backup">{newest ? formatWhen(newest.timestamp) : 'None yet'}</Fact>
        <Fact label="Compute">{compute}</Fact>
      </dl>
      {(project.failureReason || project.failureKind) && project.currentStage === 'FAILED' && <ProvisionFailure reason={project.failureReason} kind={project.failureKind} message={project.failureMessage} className="break-words text-xs text-red-400" />}
    </section>
  );
}

function latestBackup(backups: readonly BackupInfo[] | undefined): BackupInfo | undefined {
  if (!Array.isArray(backups)) return undefined;
  return [...backups].sort((a, b) => b.timestamp.localeCompare(a.timestamp))[0];
}

const BACKUP_LABEL: Record<string, string> = { COMPLETED: 'Succeeded', FAILED: 'Failed', IN_PROGRESS: 'In progress', RUNNING: 'In progress' };

function LastBackup({ projectId }: { readonly projectId: string }) {
  const [params] = useSearchParams();
  const { data } = useListBackups(projectId);
  const newest = latestBackup(data?.backups);
  const all = { pathname: '.', search: panelSearch(params, { kind: 'database' }, 'backups').toString() };
  return (
    <section aria-labelledby="db-last-backup" className={card}>
      <h3 id="db-last-backup" className="text-sm font-semibold text-text-primary">Last backup</h3>
      <p className="text-sm text-text-secondary">
        {newest ? `${BACKUP_LABEL[newest.status] ?? newest.status} · ${formatWhen(newest.timestamp)}. ` : 'No backup has run yet. '}
        <Link to={all} className="text-purple-400 hover:underline">See all backups</Link>
      </p>
    </section>
  );
}

const GOES_DOWN = [
  'Every connection to the database',
  'The GraphQL and REST API',
  'Apps in this project that use DATABASE_URL will fail until you resume',
  'Scheduled backups cannot run while paused',
];
const STAYS = ['All data and existing backups', 'Your keys, users and settings', 'Other services in the project'];

function List({ title, items }: { readonly title: string; readonly items: readonly string[] }) {
  return (
    <div>
      <h4 className="text-xs font-semibold uppercase tracking-wide text-text-tertiary">{title}</h4>
      <ul className="mt-1 list-disc space-y-0.5 pl-5 text-sm text-text-secondary">
        {items.map((item) => <li key={item}>{item}</li>)}
      </ul>
    </div>
  );
}

function PauseCard({ project }: { readonly project: DatabaseInstance }) {
  const { canAdmin, isLoading } = useProjectRole(project.projectId);
  const pause = usePauseProject();
  const resume = useResumeProject();
  const { data: tiers } = useTiers();
  const autoPauseDays = tiers?.find((t) => t.tier === project.tier)?.autoPauseAfterDays ?? 0;
  const paused = project.status === 'PAUSED';
  const busy = pause.isPending || resume.isPending;
  const failure = pause.error ?? resume.error;
  const settled = paused || project.status === 'ACTIVE';
  const [asking, setAsking] = useState(false);
  const run = () => (paused ? resume.mutate(project.projectId) : setAsking(true));
  const confirmPause = () => { setAsking(false); pause.mutate({ projectId: project.projectId }); };
  const idleIcon = paused ? PlayCircle : PauseCircle;
  const Icon = busy ? Loader2 : idleIcon;
  return (
    <section aria-labelledby="db-pause" className={card}>
      <h3 id="db-pause" className="text-sm font-semibold text-text-primary">{paused ? 'Resume this database' : 'Pause this database'}</h3>
      <p className="text-sm text-text-secondary">
        {paused
          ? 'The database is stopped. Your data and backups are kept. Resuming starts it again, which can take a few minutes.'
          : 'Pausing stops the database to save credits. A backup is taken first, and your data and backups are kept. Resuming takes a few minutes.'}
      </p>
      {!paused && (
        <div className="grid gap-4 sm:grid-cols-2">
          <List title="What goes down" items={GOES_DOWN} />
          <List title="What stays" items={STAYS} />
        </div>
      )}
      {failure != null && <p role="alert" className="break-words text-xs text-red-400">{projectOperationMessage(failure, 'The database could not be changed')}</p>}
      {busy && <output className="block text-xs text-text-secondary">{paused ? 'Resuming: starting the database.' : 'Pausing: taking a backup, then stopping the database.'} This can take a few minutes.</output>}
      <button
        type="button"
        onClick={run}
        disabled={!canAdmin || busy || !settled}
        className="inline-flex items-center gap-2 rounded-lg bg-amber-500 px-4 py-2 text-sm font-medium text-white hover:bg-amber-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Icon className={busy ? 'h-4 w-4 animate-spin' : 'h-4 w-4'} aria-hidden="true" /> {paused ? 'Resume database' : 'Pause database'}
      </button>
      <ConfirmModal
        open={asking}
        onClose={() => setAsking(false)}
        onConfirm={confirmPause}
        title="Pause this database?"
        message="Connections and the API stop until you resume. A backup is taken first and your data is kept."
        confirmLabel="Yes, pause"
      />
      {!canAdmin && !isLoading && <p className="text-xs text-text-tertiary">Only an admin of this organization can do this. Ask an admin.</p>}
      {autoPauseDays > 0 && (
        <p className="text-xs text-text-tertiary">On this plan a database with no activity for {autoPauseDays} days is paused automatically.</p>
      )}
    </section>
  );
}

function Danger({ project }: { readonly project: DatabaseInstance }) {
  const navigate = useNavigate();
  const { canAdmin } = useProjectRole(project.projectId);
  const deprovision = useDeprovisionDatabase();
  const [confirming, setConfirming] = useState(false);
  if (!canAdmin) return <p className="text-xs text-text-tertiary">Only an admin of this organization can delete this project.</p>;
  return (
    <>
      <DangerZone
        project={project}
        protectedFromDeletion={isDeletionProtected(project)}
        onDelete={() => setConfirming(true)}
        deleting={deprovision.isPending}
        deleteError={deprovision.error}
      />
      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() =>
          deprovision.mutate(project.projectId, {
            onSuccess: () => navigate('/instances'),
            onError: () => setConfirming(false),
          })
        }
        title="Delete Project"
        message={`This stops "${project.projectId}" now and permanently deletes it and all associated data after 7 days. An org owner can cancel until then.`}
        confirmText={project.projectId}
        confirmLabel="Delete Project"
        destructive
        loading={deprovision.isPending}
      />
    </>
  );
}

export function DbOverviewTab({ project }: { readonly project: DatabaseInstance }) {
  return (
    <div className="space-y-5">
      <LiveState project={project} />
      <LastBackup projectId={project.projectId} />
      <PauseCard project={project} />
      <Danger project={project} />
    </div>
  );
}
