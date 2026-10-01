import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Loader2, Pause, Play, Trash2 } from 'lucide-react';
import {
  LifecycleFailedError,
  useDeleteApp,
  useFollowLifecycle,
  usePauseApp,
  useRefreshApps,
  useResumeApp,
  type App,
  type LifecycleOperation,
  type PendingLifecycle,
} from '../../api/apps';
import { secondaryButton } from './ContainerBits';

const PENDING_TEXT: Record<LifecycleOperation, string> = {
  pause: 'Pausing',
  resume: 'Resuming',
  deletion: 'Deleting',
};

const PAUSABLE = new Set(['ACTIVE', 'FAILED', 'PAUSING']);

const canResume = (app: App): boolean =>
  (app.status === 'PAUSED' && app.replicas > 0) || app.status === 'RESUMING';

interface LifecycleActionsProps {
  readonly app: App;
  readonly deployed: boolean;
  readonly onError: (err: unknown) => void;
  // How often the app is read while an operation it started is under way.
  readonly followMs?: number;
}

// Follows the operation the server accepted until the app shows it finished or
// records why it did not; a deleted app returns to the containers list.
function useLifecycleFollow(app: App, followMs: number, onError: (err: unknown) => void) {
  const navigate = useNavigate();
  const [pending, setPending] = useState<PendingLifecycle | null>(null);
  const follow = useFollowLifecycle(app.projectId, app.id, pending, followMs);
  const refresh = useRefreshApps(app.projectId);
  const outcome = follow.data;
  const followError = follow.error;

  useEffect(() => {
    if (!pending) return;
    if (followError) {
      setPending(null);
      onError(followError);
      return;
    }
    if (!outcome || outcome.state === 'pending') return;
    setPending(null);
    if (outcome.state === 'done' && pending.operation === 'deletion') {
      navigate(`/project/${app.projectId}/containers`);
      return;
    }
    if (outcome.state === 'failed') onError(new LifecycleFailedError(outcome.reason));
    void refresh();
  }, [outcome, followError, pending, onError, navigate, refresh, app.projectId]);

  return { pending, start: setPending };
}

export function LifecycleActions({ app, deployed, onError, followMs = 3000 }: LifecycleActionsProps) {
  const pause = usePauseApp(app.projectId, app.id);
  const resume = useResumeApp(app.projectId, app.id);
  const hasDisk = app.disk !== undefined;
  const remove = useDeleteApp(app.projectId, app.id, hasDisk);
  const { pending, start } = useLifecycleFollow(app, followMs, onError);
  const [confirming, setConfirming] = useState(false);
  // Erasing a disk takes the same typed confirmation as deleting a project.
  const [typedName, setTypedName] = useState('');
  const busy = pause.isPending || resume.isPending || remove.isPending || pending !== null;
  const confirmed = !hasDisk || typedName === app.name;
  const started = (accepted: PendingLifecycle) => {
    onError(null);
    start(accepted);
  };

  const confirmDelete = () =>
    remove.mutate(undefined, {
      onSuccess: (accepted) => {
        setConfirming(false);
        started(accepted);
      },
      onError,
    });

  return (
    <>
      {pending && (
        <span
          className="flex items-center gap-1.5 text-sm text-text-secondary"
          data-testid="lifecycle-pending"
          role="status"
        >
          <Loader2 className="w-4 h-4 animate-spin" />
          {PENDING_TEXT[pending.operation]}… this can take a few minutes
        </span>
      )}
      {deployed && PAUSABLE.has(app.status) && (
        <button
          type="button"
          onClick={() => pause.mutate(undefined, { onSuccess: started, onError })}
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
          onClick={() => resume.mutate(undefined, { onSuccess: started, onError })}
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
