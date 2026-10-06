import { useRef } from 'react';
import { apiErrorMessage, useSetAutoDeploy, type App } from '../../api/apps';
import { formatWhen } from './appCopy';
import { shortDigest } from './pipelineCopy';

const isPinned = (image: string) => image.includes('@');

function WatchState({ app }: { readonly app: App }) {
  const watch = app.imageWatch;
  if (!app.autoDeploy) return null;
  return (
    <div className="mt-3 space-y-1 text-xs text-text-tertiary" data-testid="image-watch">
      <p>
        Watching <span className="font-mono text-text-secondary">{app.image}</span>
        {watch?.digest && (
          <>
            {' '}
            · last seen <span className="font-mono">{shortDigest(watch.digest)}</span>
          </>
        )}
        {watch?.checkedAt && <> · checked {formatWhen(watch.checkedAt)}</>}
      </p>
      {!watch && <p>The first check runs within a few minutes.</p>}
      {watch?.error && (
        <p className="text-amber-400" data-testid="image-watch-error">
          The last check failed: {watch.error}. It is retried later, waiting longer each time.
        </p>
      )}
    </div>
  );
}

export function AutoDeployCard({ app }: { readonly app: App }) {
  const setAutoDeploy = useSetAutoDeploy(app.projectId, app.id);
  const pinned = isPinned(app.image);
  // Shows the choice at once; the server's answer then confirms or reverts it.
  const checked = setAutoDeploy.isPending ? !!setAutoDeploy.variables?.autoDeploy : !!app.autoDeploy;
  // The toggle disables a render late; without this a quick second click sends a second change.
  const inFlight = useRef(false);
  const toggle = (autoDeploy: boolean) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setAutoDeploy.mutate(
      { version: app.version, autoDeploy },
      { onSettled: () => { inFlight.current = false; } },
    );
  };
  return (
    <section className="bg-surface-card border border-border-primary rounded-lg p-4">
      <label className="grid grid-cols-[auto_1fr] gap-x-3 items-start">
        <input
          type="checkbox"
          className="mt-1"
          checked={checked}
          disabled={pinned || setAutoDeploy.isPending}
          onChange={(event) => toggle(event.target.checked)}
          data-testid="auto-deploy-toggle"
        />
        <span className="text-sm font-medium text-text-primary">
          Auto-deploy when the image tag changes
        </span>
        <span className="col-start-2 text-xs text-text-tertiary">
          Every few minutes the tag is looked up in its registry, with this project's saved
          registry credential when there is one. When it points at a new digest, that digest is
          deployed.
        </span>
      </label>
      {pinned && (
        <p className="mt-3 text-xs text-text-tertiary" data-testid="auto-deploy-pinned">
          This image is pinned by digest, which never changes. Edit the container to name a tag,
          such as {app.image.split('@')[0]}:main, to watch it.
        </p>
      )}
      <WatchState app={app} />
      {setAutoDeploy.error && (
        <p role="alert" className="mt-3 text-sm text-red-400">
          {apiErrorMessage(setAutoDeploy.error, 'Auto-deploy could not be changed')}
        </p>
      )}
    </section>
  );
}
