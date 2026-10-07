import { useState } from 'react';
import { Clock, RotateCcw } from 'lucide-react';
import {
  useRestoreFromBackup,
  useRestoreJob,
  RESTORE_LIMIT_CODE,
  RESTORE_RUNNING,
  type RestoreMode,
  type RestoreRequest,
  type RestoreResult,
} from '../../hooks/useProvisioning';
import { Button } from '../Button';
import { toZonedInstant } from '../../utils/zonedInstant';
import { serverErrorCode, serverErrorMessage } from '../../utils/serverError';

type RestoreTone = 'pending' | 'ok' | 'error';

const TONE_CLASSES: Record<RestoreTone, string> = {
  pending: 'bg-blue-900/20 border-blue-500/30 text-blue-300',
  ok: 'bg-green-900/20 border-green-500/30 text-green-400',
  error: 'bg-red-900/20 border-red-500/30 text-red-400',
};

// What each phase a restore job reports means to the person waiting on it.
// Any other step is the platform's own business and is not shown.
const STEP_TEXT: Record<string, string> = {
  RESTORING_DATABASE: 'Restoring the database',
  SAFETY_BACKUP: 'Taking a backup of the current data first',
  REPLACING_DATABASE: 'Replacing the database; the project is offline until this finishes',
  CHECKING_DATABASE: 'Checking the restored database',
  ROLLING_BACK: 'The restore did not complete; putting the project back as it was',
};

const inputClass =
  'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary';

// A started restore answers RUNNING and is polled to COMPLETED or FAILED.
function describeRestore(result: RestoreResult): { tone: RestoreTone; message: string } {
  const inPlace = result.mode === 'in_place';
  const step = result.currentStep ? STEP_TEXT[result.currentStep] : undefined;
  switch (result.status) {
    case RESTORE_RUNNING: {
      const started = inPlace ? 'Restore started.' : 'Restore started. The new project is being created.';
      return { tone: 'pending', message: step ? `${started} ${step}…` : started };
    }
    case 'COMPLETED':
      return {
        tone: 'ok',
        message: inPlace ? "Restore completed. This project's data has been restored." : 'Restore completed. The new project is ready.',
      };
    case 'FAILED':
      return { tone: 'error', message: `The restore failed: ${result.failureReason || 'no reason was recorded.'}` };
    default:
      return result.failureReason
        ? { tone: 'error', message: `The restore failed: ${result.failureReason}` }
        : { tone: 'ok', message: result.message || 'Restore started.' };
  }
}

function RestoreResultPanel({ result }: { readonly result: RestoreResult }) {
  const { tone, message } = describeRestore(result);
  const newProject = result.mode === 'in_place' ? undefined : result.newProjectId ?? result.projectId;
  return (
    <output data-testid="restore-result" data-tone={tone} className={`block p-3 rounded-lg border text-sm ${TONE_CLASSES[tone]}`}>
      <p className="font-medium">{message}</p>
      {newProject && (
        <p className="mt-1 text-xs text-text-secondary">New project: <span className="font-mono text-text-primary">{newProject}</span></p>
      )}
      {result.recoveryType && (
        <p className="text-xs text-text-secondary">Recovery type: <span className="font-mono text-text-primary">{result.recoveryType}</span></p>
      )}
    </output>
  );
}

interface Refusal {
  readonly message: string;
  readonly code?: string;
}

function RefusalPanel({ refusal, onReplaceInstead }: { readonly refusal: Refusal; readonly onReplaceInstead: () => void }) {
  return (
    <div role="alert" className={`p-3 rounded-lg border text-sm space-y-2 ${TONE_CLASSES.error}`}>
      <p>{refusal.message}</p>
      {refusal.code === RESTORE_LIMIT_CODE && (
        <Button size="sm" variant="secondary" onClick={onReplaceInstead}>
          Restore into this project instead
        </Button>
      )}
    </div>
  );
}

function ModeChoice({ mode, onChange }: { readonly mode: RestoreMode; readonly onChange: (mode: RestoreMode) => void }) {
  const option = (value: RestoreMode, label: string, hint: string) => (
    <label className="flex items-start gap-2 text-sm text-text-primary cursor-pointer">
      <input type="radio" name="restore-mode" value={value} checked={mode === value} onChange={() => onChange(value)} className="mt-1" />
      <span>
        {label}
        <span className="block text-xs text-text-secondary">{hint}</span>
      </span>
    </label>
  );
  return (
    <fieldset className="space-y-2">
      <legend className="text-xs text-text-secondary mb-1">Restore into</legend>
      {option('new_project', 'A new project', 'A copy beside this one. Needs a free project on your plan.')}
      {option('in_place', 'This project', "Replaces this project's data. Its address, keys and apps stay the same.")}
    </fieldset>
  );
}

// Restores this project's backups into a new project, or into this project
// itself, which a plan with no free project slot can still do.
export function RestoreForm({ projectId }: { readonly projectId: string }) {
  const restore = useRestoreFromBackup(projectId);
  const [mode, setMode] = useState<RestoreMode>('new_project');
  const [newProjectName, setNewProjectName] = useState('');
  const [targetTime, setTargetTime] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [refusal, setRefusal] = useState<Refusal | null>(null);

  const startedJobId = restore.data?.status === RESTORE_RUNNING ? restore.data.id : undefined;
  const restoreJob = useRestoreJob(projectId, startedJobId);
  const result = restoreJob.data ?? restore.data;
  const running = restore.isPending || result?.status === RESTORE_RUNNING;
  const isPitr = targetTime.trim() !== '';
  const inPlace = mode === 'in_place';
  const ready = inPlace ? confirmed : newProjectName.trim() !== '';

  function chooseMode(next: RestoreMode) {
    setMode(next);
    setRefusal(null);
  }

  function submit() {
    setRefusal(null);
    // datetime-local only yields a valid local time or ''.
    const at = toZonedInstant(targetTime);
    const request: RestoreRequest = inPlace
      ? { mode: 'in_place', confirmReplace: true, targetTime: at }
      : { newProjectName, targetTime: at };
    restore.mutate(request, {
      onError: (e: unknown) =>
        setRefusal({ message: serverErrorMessage(e, 'The restore was not started'), code: serverErrorCode(e) }),
    });
  }

  let label = isPitr ? 'Restore to Point in Time' : 'Restore Latest Backup';
  if (inPlace) label = "Replace this project's data";
  if (restore.isPending) label = 'Starting the restore…';
  else if (running) label = 'Restore running…';

  return (
    <div className="max-w-lg space-y-5">
      <p className="text-sm text-text-secondary">
        Leave the target time blank to restore the latest backup, or set a time for Point-in-Time Recovery (PITR).
      </p>

      <div className="space-y-4">
        <ModeChoice mode={mode} onChange={chooseMode} />

        {!inPlace && (
          <div>
            <label htmlFor="restore-new-name" className="text-xs text-text-secondary block mb-1">New project name *</label>
            <input
              id="restore-new-name"
              value={newProjectName}
              onChange={(e) => setNewProjectName(e.target.value)}
              placeholder="e.g. orders restored"
              className={inputClass}
            />
          </div>
        )}

        <div>
          <label htmlFor="restore-target-time" className="text-xs text-text-secondary block mb-1 flex items-center gap-1.5">
            <Clock className="w-3 h-3" /> Target Time (PITR) — leave blank for latest backup
          </label>
          <input
            id="restore-target-time"
            type="datetime-local"
            step={1}
            value={targetTime}
            onChange={(e) => setTargetTime(e.target.value)}
            className={inputClass}
          />
          {isPitr && <p className="text-xs text-accent-primary mt-1">Point-in-Time Recovery mode enabled</p>}
        </div>

        {inPlace && (
          <label className="flex items-start gap-2 text-sm text-text-primary">
            <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} className="mt-1" />
            <span>
              I understand: this project's data is replaced with the data as it was at the restore point, and changes
              made after it are lost. A backup of the current data is taken first. The project is offline for a few
              minutes.
            </span>
          </label>
        )}
      </div>

      {refusal && <RefusalPanel refusal={refusal} onReplaceInstead={() => chooseMode('in_place')} />}
      {result && <RestoreResultPanel result={result} />}

      <Button variant={inPlace ? 'danger' : 'primary'} disabled={!ready || running || !projectId} onClick={submit}>
        <RotateCcw className="w-4 h-4 mr-2" />
        {label}
      </Button>
    </div>
  );
}
