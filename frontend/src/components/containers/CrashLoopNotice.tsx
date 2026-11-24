import { useEffect, useState } from 'react';
import { Loader2, Play } from 'lucide-react';
import {
  LifecycleFailedError,
  useFollowLifecycle,
  useRefreshApps,
  useResumeApp,
  type App,
  type PendingLifecycle,
} from '../../api/apps';
import { primaryButton } from './ContainerBits';

interface CrashLoopNoticeProps {
  readonly app: App;
  // Starting an app is a developer's action, as resuming it is.
  readonly canStart: boolean;
  readonly onError: (err: unknown) => void;
  readonly followMs?: number;
}

// Shown while the platform keeps an app stopped because it crashed over and
// over (EXC-839). Start again is the ordinary resume, which clears the stop.
export function CrashLoopNotice({ app, canStart, onError, followMs = 3000 }: CrashLoopNoticeProps) {
  const resume = useResumeApp(app.projectId, app.id);
  const [pending, setPending] = useState<PendingLifecycle | null>(null);
  const follow = useFollowLifecycle(app.projectId, app.id, pending, followMs);
  const refresh = useRefreshApps(app.projectId);
  const outcome = follow.data;

  useEffect(() => {
    if (!pending || !outcome || outcome.state === 'pending') return;
    setPending(null);
    if (outcome.state === 'failed') onError(new LifecycleFailedError(outcome.reason));
    void refresh();
  }, [outcome, pending, onError, refresh]);

  const started = (accepted: PendingLifecycle) => {
    onError(null);
    setPending(accepted);
  };

  const suspension = app.suspension;
  if (suspension?.kind !== 'crash-loop' || app.status !== 'PAUSED') return null;
  const busy = resume.isPending || pending !== null;

  return (
    <div
      role="alert"
      className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
      data-testid="crash-loop-notice"
    >
      <span data-testid="crash-loop-text">Stopped: {suspension.reason}</span>
      {canStart && (
        <button
          type="button"
          disabled={busy}
          className={primaryButton}
          data-testid="start-again-button"
          onClick={() => resume.mutate(undefined, { onSuccess: started, onError })}
        >
          {busy ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4" />} Start again
        </button>
      )}
    </div>
  );
}
