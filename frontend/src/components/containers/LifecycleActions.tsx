import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Pause, Play, Trash2 } from 'lucide-react';
import { useDeleteApp, usePauseApp, useResumeApp, type App } from '../../api/apps';
import { secondaryButton } from './ContainerBits';

const PAUSABLE = new Set(['ACTIVE', 'FAILED', 'PAUSING']);

const canResume = (app: App): boolean =>
  (app.status === 'PAUSED' && app.replicas > 0) || app.status === 'RESUMING';

interface LifecycleActionsProps {
  readonly app: App;
  readonly deployed: boolean;
  readonly onError: (err: unknown) => void;
}

export function LifecycleActions({ app, deployed, onError }: LifecycleActionsProps) {
  const navigate = useNavigate();
  const pause = usePauseApp(app.projectId, app.id);
  const resume = useResumeApp(app.projectId, app.id);
  const hasDisk = app.disk !== undefined;
  const remove = useDeleteApp(app.projectId, app.id, hasDisk);
  const [confirming, setConfirming] = useState(false);
  // Erasing a disk takes the same typed confirmation as deleting a project.
  const [typedName, setTypedName] = useState('');
  const busy = pause.isPending || resume.isPending || remove.isPending;
  const confirmed = !hasDisk || typedName === app.name;

  const confirmDelete = () =>
    remove.mutate(undefined, {
      onSuccess: () => navigate(`/project/${app.projectId}/containers`),
      onError,
    });

  return (
    <>
      {deployed && PAUSABLE.has(app.status) && (
        <button
          type="button"
          onClick={() => pause.mutate(undefined, { onError })}
          disabled={busy}
          className={secondaryButton}
          data-testid="pause-button"
        >
          <Pause className="w-4 h-4" /> Pause
        </button>
      )}
      {deployed && canResume(app) && (
        <button
          type="button"
          onClick={() => resume.mutate(undefined, { onError })}
          disabled={busy}
          className={secondaryButton}
          data-testid="resume-button"
        >
          <Play className="w-4 h-4" /> Resume
        </button>
      )}
      {confirming ? (
        <span className="flex items-center gap-2 text-sm">
          <span className="text-text-secondary" data-testid="delete-confirm-text">
            {hasDisk
              ? `This stops the container and removes it with its history, secret values and its disk: every file on the disk is erased. Type ${app.name} to confirm.`
              : 'This stops the container and removes it with its history and secret values.'}
          </span>
          {hasDisk && (
            <input
              value={typedName}
              onChange={(e) => setTypedName(e.target.value)}
              aria-label="Type the container name to confirm"
              className="w-32 px-2 py-1 rounded-md bg-bg-tertiary border border-border-primary text-text-primary"
              data-testid="delete-confirm-name"
            />
          )}
          <button
            type="button"
            onClick={confirmDelete}
            disabled={busy || !confirmed}
            className="px-3 py-1.5 rounded-lg bg-red-600 text-white text-sm hover:bg-red-500 disabled:opacity-50"
            data-testid="delete-confirm"
          >
            Delete
          </button>
          <button
            type="button"
            onClick={() => {
              setConfirming(false);
              setTypedName('');
            }}
            className={secondaryButton}
            data-testid="delete-cancel"
          >
            Cancel
          </button>
        </span>
      ) : (
        <button
          type="button"
          onClick={() => setConfirming(true)}
          disabled={busy}
          className={secondaryButton}
          data-testid="delete-button"
        >
          <Trash2 className="w-4 h-4" /> Delete
        </button>
      )}
    </>
  );
}
