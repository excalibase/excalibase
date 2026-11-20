import { useState } from 'react';
import { useAppBuild, useAppBuilds, type AppBuild } from '../../api/appBuilds';
import { buildStatus } from '../sites/SiteBuildList';
import { formatWhen } from './appCopy';
import { Spinner, ToneBadge, secondaryButton } from './ContainerBits';

function BuildLog({
  projectId,
  appId,
  buildId,
}: {
  readonly projectId: string;
  readonly appId: string;
  readonly buildId: string;
}) {
  const { data: build, error } = useAppBuild(projectId, appId, buildId);
  if (error) return <p className="text-xs text-red-400">The log could not be loaded</p>;
  if (!build) return <Spinner />;
  return (
    <pre
      className="mt-2 max-h-96 overflow-auto rounded-md bg-black/40 p-3 text-xs text-text-secondary whitespace-pre-wrap"
      data-testid={`app-build-log-${buildId}`}
    >
      {build.log || 'No output yet.'}
    </pre>
  );
}

function BuildRow({
  projectId,
  appId,
  build,
  canReadLogs,
}: {
  readonly projectId: string;
  readonly appId: string;
  readonly build: AppBuild;
  readonly canReadLogs: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <li className="px-4 py-3" data-testid={`app-build-${build.id}`}>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm text-text-primary truncate">
            <code>{build.commitSha.slice(0, 7)}</code> {build.commitMessage?.split('\n')[0] ?? ''}
          </p>
          <p className="text-xs text-text-tertiary">
            {build.trigger === 'push' ? 'Push to' : 'Manual build of'}{' '}
            {build.app?.branch || 'the default branch'} · {formatWhen(build.createdAt)}
          </p>
        </div>
        <div className="flex items-center gap-2 flex-shrink-0">
          <ToneBadge
            {...buildStatus({ status: build.status })}
            testId={`app-build-status-${build.id}`}
          />
          {canReadLogs && (
            <button
              type="button"
              className={secondaryButton}
              onClick={() => setOpen(!open)}
              data-testid={`app-build-toggle-${build.id}`}
            >
              {open ? 'Hide log' : 'Log'}
            </button>
          )}
        </div>
      </div>
      {build.failureReason && (
        <p className="mt-1 text-xs text-red-400" data-testid={`app-build-reason-${build.id}`}>
          {build.failureReason}
        </p>
      )}
      {open && <BuildLog projectId={projectId} appId={appId} buildId={build.id} />}
    </li>
  );
}

// Logs are developers only, as on the server.
export function AppBuildList({
  projectId,
  appId,
  canReadLogs,
}: {
  readonly projectId: string;
  readonly appId: string;
  readonly canReadLogs: boolean;
}) {
  const { data: builds, error } = useAppBuilds(projectId, appId);
  if (error) return <p className="text-xs text-red-400">The builds could not be loaded</p>;
  if (!builds) return <Spinner />;
  if (builds.length === 0) {
    return (
      <p className="px-4 py-6 text-sm text-text-tertiary">
        No builds yet. Push to the branch or rebuild.
      </p>
    );
  }
  return (
    <ul className="divide-y divide-border-primary" data-testid="app-builds">
      {builds.map((build) => (
        <BuildRow
          key={build.id}
          projectId={projectId}
          appId={appId}
          build={build}
          canReadLogs={canReadLogs}
        />
      ))}
    </ul>
  );
}
