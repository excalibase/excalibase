import { useState } from 'react';
import { useSiteBuild, useSiteBuilds, type SiteBuild } from '../../api/siteBuilds';
import { formatWhen, type Tone } from '../containers/appCopy';
import { Spinner, ToneBadge, secondaryButton } from '../containers/ContainerBits';

export function buildStatus(build: SiteBuild): { label: string; tone: Tone } {
  switch (build.status) {
    case 'QUEUED':
      return { label: 'Queued', tone: 'neutral' };
    case 'RUNNING':
      return { label: 'Building', tone: 'progress' };
    case 'SUCCEEDED':
      return { label: 'Deployed', tone: 'success' };
    case 'CANCELLED':
      return { label: 'Cancelled', tone: 'neutral' };
    default:
      return { label: 'Failed', tone: 'error' };
  }
}

function BuildLog({
  projectId,
  siteId,
  buildId,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly buildId: string;
}) {
  const { data: build, error } = useSiteBuild(projectId, siteId, buildId);
  if (error) return <p className="text-xs text-red-400">The log could not be loaded</p>;
  if (!build) return <Spinner />;
  return (
    <pre
      className="mt-2 max-h-96 overflow-auto rounded-md bg-black/40 p-3 text-xs text-text-secondary whitespace-pre-wrap"
      data-testid={`site-build-log-${buildId}`}
    >
      {build.log || 'No output yet.'}
    </pre>
  );
}

function BuildRow({
  projectId,
  siteId,
  build,
  canReadLogs,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly build: SiteBuild;
  readonly canReadLogs: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <li className="px-4 py-3" data-testid={`site-build-${build.id}`}>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm text-text-primary truncate">
            <code>{build.commitSha.slice(0, 7)}</code> {build.commitMessage?.split('\n')[0] ?? ''}
          </p>
          <p className="text-xs text-text-tertiary">
            {build.trigger === 'push' ? 'Push' : 'Rebuild'} to {build.settings.branch} ·{' '}
            {formatWhen(build.createdAt)}
          </p>
        </div>
        <div className="flex items-center gap-2 flex-shrink-0">
          <ToneBadge {...buildStatus(build)} testId={`site-build-status-${build.id}`} />
          {canReadLogs && (
            <button
              type="button"
              className={secondaryButton}
              onClick={() => setOpen(!open)}
              data-testid={`site-build-toggle-${build.id}`}
            >
              {open ? 'Hide log' : 'Log'}
            </button>
          )}
        </div>
      </div>
      {build.failureReason && (
        <p className="mt-1 text-xs text-red-400" data-testid={`site-build-reason-${build.id}`}>
          {build.failureReason}
        </p>
      )}
      {open && <BuildLog projectId={projectId} siteId={siteId} buildId={build.id} />}
    </li>
  );
}

// Logs are developers only, as on the server.
export function SiteBuildList({
  projectId,
  siteId,
  canReadLogs,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly canReadLogs: boolean;
}) {
  const { data: builds, error } = useSiteBuilds(projectId, siteId);
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
    <ul className="divide-y divide-border-primary" data-testid="site-builds">
      {builds.map((build) => (
        <BuildRow
          key={build.id}
          projectId={projectId}
          siteId={siteId}
          build={build}
          canReadLogs={canReadLogs}
        />
      ))}
    </ul>
  );
}
