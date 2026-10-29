import { useState } from 'react';
import { ArrowUpCircle, Loader2 } from 'lucide-react';
import { useMajorUpgrade, useStartMajorUpgrade } from '../hooks/useMajorUpgrade';
import { useProjectRole } from '../hooks/useProjectRole';
import { ConfirmModal } from './ui/ConfirmModal';
import { serverErrorMessage } from '../utils/serverError';
import type { DatabaseInstance, MajorUpgradeInfo, MajorUpgradeRun, MajorUpgradeStep } from '../types';

interface MajorUpgradeCardProps {
  readonly project: DatabaseInstance;
}

const STEP_LABEL: Record<MajorUpgradeStep, string> = {
  UPGRADE_CHECKING: 'Checking',
  UPGRADE_BACKUP: 'Taking a backup',
  UPGRADE_CONVERTING: 'Converting the data',
  UPGRADE_STARTING: 'Starting the database',
  UPGRADE_FINISHING: 'Finishing (statistics, extensions, new backup)',
  UPGRADE_ROLLING_BACK: 'Rolling back',
  UPGRADE_WAITING: 'Still converting; checked again every few minutes',
};

const engineName = (engine: string) => (engine.toUpperCase() === 'MYSQL' ? 'MySQL' : 'PostgreSQL');

const newestFirst = (versions: string[]) => [...versions].sort((a, b) => Number(b) - Number(a));

function startRefusal(error: unknown) {
  const blockers = (error as { response?: { data?: { blockers?: unknown } } } | null)?.response?.data?.blockers;
  return {
    message: serverErrorMessage(error, 'The upgrade was not started'),
    blockers: Array.isArray(blockers) ? blockers.filter((item): item is string => typeof item === 'string') : [],
  };
}

function RunOutcome({ run }: { readonly run: MajorUpgradeRun }) {
  const engine = engineName(run.engine);
  if (run.status === 'RUNNING') {
    return (
      <div className="flex items-center gap-2 text-sm text-blue-400 mb-3" data-testid="major-upgrade-running">
        <Loader2 className="w-4 h-4 animate-spin" />
        Upgrading to {engine} {run.toVersion}: {run.step ? (STEP_LABEL[run.step] ?? run.step) : 'Working'}
      </div>
    );
  }
  if (run.status === 'COMPLETED') {
    return (
      <div className="text-sm text-green-400 mb-3" data-testid="major-upgrade-completed">
        <p>Upgraded to {engine} {run.toVersion}.</p>
        {(run.warnings?.length ?? 0) > 0 && (
          <ul className="list-disc pl-5 mt-1 text-xs text-amber-400">
            {run.warnings?.map((warning, index) => <li key={`${index}-${warning}`}>{warning}</li>)}
          </ul>
        )}
      </div>
    );
  }
  if (run.status === 'ROLLED_BACK') {
    return (
      <div className="text-sm text-amber-400 mb-3" data-testid="major-upgrade-rolled-back">
        <p>The upgrade did not complete; the database was put back on {engine} {run.fromVersion}.</p>
        {run.error && <p className="text-xs mt-1">{run.error}</p>}
      </div>
    );
  }
  return (
    <div role="alert" className="rounded-md border border-red-500/30 bg-red-500/10 p-3 mb-3" data-testid="major-upgrade-failed">
      <p className="text-sm font-medium text-red-400">{run.error || 'The upgrade failed.'}</p>
      {run.safetyBackupId && (
        <p className="text-xs text-text-secondary mt-1">
          The backup taken before the upgrade is <span className="font-mono">{run.safetyBackupId}</span>. Restore it
          into a new project to get the data back.
        </p>
      )}
    </div>
  );
}

interface UpgradeFormProps {
  readonly project: DatabaseInstance;
  readonly info: MajorUpgradeInfo;
}

function UpgradeForm({ project, info }: UpgradeFormProps) {
  const targets = newestFirst(info.targets);
  const [picked, setPicked] = useState(targets[0]);
  // A target list refreshed under the form keeps the choice only if still offered.
  const target = targets.includes(picked) ? picked : targets[0];
  const [confirming, setConfirming] = useState(false);
  const role = useProjectRole(project.projectId);
  const start = useStartMajorUpgrade(project.projectId);
  const engine = engineName(info.engine);
  const confirmName = project.projectName || project.projectId;
  const blocked = info.blockers.length > 0;
  const refusal = start.isError ? startRefusal(start.error) : null;

  return (
    <>
      <label className="block text-xs text-text-secondary mb-3">
        Upgrade to{' '}
        <select
          value={target}
          onChange={(event) => setPicked(event.target.value)}
          className="ml-1 px-2 py-1 rounded-md border border-border-primary bg-bg-primary text-text-primary text-sm"
          data-testid="major-upgrade-target"
        >
          {targets.map((version) => (
            <option key={version} value={version}>{engine} {version}</option>
          ))}
        </select>
      </label>

      {blocked && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 mb-3 text-xs text-amber-400" data-testid="major-upgrade-blockers">
          <p className="font-medium">These must be fixed before upgrading:</p>
          <ul className="list-disc pl-5 mt-1">
            {info.blockers.map((blocker, index) => <li key={`${index}-${blocker}`}>{blocker}</li>)}
          </ul>
        </div>
      )}

      {info.downtime && (
        <p className="text-xs text-text-secondary mb-2" data-testid="major-upgrade-downtime">{info.downtime}</p>
      )}
      <ul className="list-disc pl-5 text-xs text-text-secondary mb-3" data-testid="major-upgrade-warning">
        <li>A backup is taken first, before anything changes.</li>
        <li>
          The upgrade cannot be undone in place. The way back is restoring that backup into a new project on {engine}{' '}
          {info.currentVersion}.
        </li>
        <li>Point-in-time restore cannot reach back across the upgrade.</li>
      </ul>

      {project.status !== 'ACTIVE' && (
        <p className="text-xs text-text-tertiary mb-2" data-testid="major-upgrade-not-active">
          The project must be running to upgrade it.
        </p>
      )}

      {!role.isLoading && !role.canAdmin && (
        <p className="text-xs text-text-tertiary mb-2" data-testid="major-upgrade-admin-only">
          Only org admins can start an upgrade.
        </p>
      )}

      <button
        type="button"
        onClick={() => setConfirming(true)}
        disabled={!role.canAdmin || blocked || start.isPending || project.status !== 'ACTIVE'}
        data-testid="major-upgrade-btn"
        className="flex items-center gap-2 px-4 py-2 bg-accent-primary hover:opacity-90 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {start.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <ArrowUpCircle className="w-4 h-4" />}
        Upgrade to {engine} {target}
      </button>

      {refusal && (
        <div className="text-xs text-color-error mt-3" data-testid="major-upgrade-start-error">
          <p>{refusal.message}</p>
          {refusal.blockers.length > 0 && (
            <ul className="list-disc pl-5 mt-1">
              {refusal.blockers.map((blocker, index) => <li key={`${index}-${blocker}`}>{blocker}</li>)}
            </ul>
          )}
        </div>
      )}

      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          start.mutate(target);
        }}
        title={`Upgrade to ${engine} ${target}`}
        message={[
          `This moves "${confirmName}" from ${engine} ${info.currentVersion} to ${target}.`,
          info.downtime,
          'A backup is taken first. The upgrade cannot be undone in place.',
        ].filter(Boolean).join(' ')}
        confirmText={confirmName}
        confirmLabel="Upgrade"
        destructive
        loading={start.isPending}
      />
    </>
  );
}

// Moves a project to a newer major. The run outlives the request, so the card
// shows the state the control plane reports and polls while it runs.
export function MajorUpgradeCard({ project }: MajorUpgradeCardProps) {
  const { data: info, isLoading, error } = useMajorUpgrade(project.projectId);

  let body: React.ReactNode;
  if (isLoading) {
    body = <Loader2 className="w-4 h-4 animate-spin text-text-tertiary" />;
  } else if (!info) {
    body = (
      <p className="text-xs text-color-error" data-testid="major-upgrade-load-error">
        {serverErrorMessage(error, 'The upgrade options could not be loaded')}
      </p>
    );
  } else {
    const running = info.latest?.status === 'RUNNING';
    const reason = info.supported
      ? info.reason || 'This project is already on the newest version offered.'
      : info.reason;
    const canOffer = info.supported && info.targets.length > 0;
    body = (
      <>
        <p className="text-xs text-text-secondary mb-3">
          This project runs {engineName(info.engine)} {info.currentVersion}.
        </p>
        {info.latest && <RunOutcome run={info.latest} />}
        {!running && !canOffer && (
          <p className="text-xs text-text-secondary" data-testid="major-upgrade-reason">{reason}</p>
        )}
        {!running && canOffer && <UpgradeForm project={project} info={info} />}
      </>
    );
  }

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="major-upgrade-card">
      <h4 className="text-sm font-medium text-text-primary mb-2">Major version upgrade</h4>
      {body}
    </div>
  );
}
