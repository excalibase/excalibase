import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Copy } from 'lucide-react';
import {
  useCopyDatabase,
  useRestoreJob,
  RESTORE_RUNNING,
  type CopyPoint,
  type CopyRequest,
  type RestoreResult,
} from '../../hooks/useProvisioning';
import { Button } from '../Button';
import { toLocalInput, toZonedInstant } from '../../utils/zonedInstant';
import { serverErrorMessage } from '../../utils/serverError';
import type { RestoreWindow } from './RestoreForm';

const inputClass =
  'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-accent-primary';

// What a copy's job reports while it runs; other steps are not shown.
const STEP_TEXT: Record<string, string> = {
  SOURCE_BACKUP: 'Taking a backup of the database as it is now',
  RESTORING_DATABASE: 'Building the copy',
};

const POINTS: ReadonlyArray<{ value: CopyPoint; label: string; hint: string }> = [
  { value: 'now', label: 'As it is now', hint: 'A backup is taken first, and the copy is made from it.' },
  { value: 'latest', label: 'Latest backup', hint: 'The most recent backup this database already has.' },
  { value: 'time', label: 'A point in time', hint: 'Any time the backups reach.' },
];

function PointChoice({ point, onChange }: { readonly point: CopyPoint; readonly onChange: (point: CopyPoint) => void }) {
  return (
    <fieldset className="space-y-2">
      <legend className="text-xs text-text-secondary mb-1">Copy the data</legend>
      {POINTS.map(({ value, label, hint }) => (
        <label key={value} className="flex items-start gap-2 text-sm text-text-primary cursor-pointer">
          <input type="radio" name="copy-point" value={value} checked={point === value} onChange={() => onChange(value)} className="mt-1" />
          <span>
            {label}
            <span className="block text-xs text-text-secondary">{hint}</span>
          </span>
        </label>
      ))}
    </fieldset>
  );
}

function CopyProgress({ job }: { readonly job: RestoreResult }) {
  const copyId = job.newProjectId;
  if (job.status === 'COMPLETED') {
    return (
      <output data-testid="copy-result" className="block p-3 rounded-lg border text-sm bg-green-900/20 border-green-500/30 text-green-400">
        <p className="font-medium">The copy is ready.</p>
        {copyId && (
          <Link to={`/project/${copyId}`} className="text-accent-primary hover:underline">
            Open the copy
          </Link>
        )}
      </output>
    );
  }
  if (job.status === 'FAILED') {
    return (
      <output data-testid="copy-result" className="block p-3 rounded-lg border text-sm bg-red-900/20 border-red-500/30 text-red-400">
        The copy failed: {job.failureReason || 'no reason was recorded.'}
      </output>
    );
  }
  const step = job.currentStep ? STEP_TEXT[job.currentStep] : undefined;
  return (
    <output data-testid="copy-result" className="block p-3 rounded-lg border text-sm bg-blue-900/20 border-blue-500/30 text-blue-300">
      Copy started{step ? `. ${step}…` : '.'}
      {copyId && <span className="block mt-1 text-xs text-text-secondary">New project: <span className="font-mono">{copyId}</span></span>}
    </output>
  );
}

interface CopyDatabaseFormProps {
  readonly projectId: string;
  // A MySQL copy at a time stays inside the backup window (ADR 0041).
  readonly engine?: 'mysql';
  readonly restoreWindow?: RestoreWindow;
}

// Copies this database into a new project in the same organisation (EXC-667).
export function CopyDatabaseForm({ projectId, engine, restoreWindow }: CopyDatabaseFormProps) {
  const copy = useCopyDatabase(projectId);
  const [name, setName] = useState('');
  const [point, setPoint] = useState<CopyPoint>('now');
  const [targetTime, setTargetTime] = useState('');
  const [refusal, setRefusal] = useState<string | null>(null);

  const startedJobId = copy.data?.status === RESTORE_RUNNING ? copy.data.id : undefined;
  const job = useRestoreJob(projectId, startedJobId).data ?? copy.data;
  const running = copy.isPending || job?.status === RESTORE_RUNNING;
  const atTime = point === 'time';
  const ready = name.trim() !== '' && (!atTime || targetTime.trim() !== '');
  const bounds = engine === 'mysql' && restoreWindow
    ? { min: toLocalInput(restoreWindow.earliest), max: toLocalInput(restoreWindow.latest) }
    : {};

  function submit() {
    setRefusal(null);
    const request: CopyRequest = atTime
      ? { name: name.trim(), point, targetTime: toZonedInstant(targetTime) }
      : { name: name.trim(), point };
    copy.mutate(request, { onError: (e: unknown) => setRefusal(serverErrorMessage(e, 'The copy was not started')) });
  }

  return (
    <div className="max-w-lg space-y-5">
      <p className="text-sm text-text-secondary">
        Makes a new project in this organisation holding a copy of this database. It has its own address and
        credentials, and counts toward your plan&apos;s projects and storage.
      </p>
      <div className="space-y-4">
        <div>
          <label htmlFor="copy-name" className="text-xs text-text-secondary block mb-1">New project name *</label>
          <input id="copy-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. orders staging" className={inputClass} />
        </div>
        <PointChoice point={point} onChange={setPoint} />
        {atTime && (
          <div>
            <label htmlFor="copy-target-time" className="text-xs text-text-secondary block mb-1">Copy at</label>
            <input id="copy-target-time" type="datetime-local" step={1} value={targetTime} onChange={(e) => setTargetTime(e.target.value)} className={inputClass} {...bounds} />
          </div>
        )}
        <p data-testid="copy-logins-note" className="text-xs text-text-tertiary">
          The platform gives the copy new credentials. Logins you created inside the database keep their passwords in
          the copy.
        </p>
      </div>
      {refusal && (
        <div role="alert" className="p-3 rounded-lg border text-sm bg-red-900/20 border-red-500/30 text-red-400">
          {refusal}
        </div>
      )}
      {job && <CopyProgress job={job} />}
      <Button disabled={!ready || running || !projectId} onClick={submit}>
        <Copy className="w-4 h-4 mr-2" />
        Copy database
      </Button>
    </div>
  );
}
