import { Link, useParams } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { useApps, useDeploys, apiErrorMessage, type App } from '../api/apps';
import { useAppHostingEnabled } from '../hooks/useDeploymentMode';
import { appDisplayStatus, formatWhen } from '../components/containers/appCopy';
import {
  ContainersHeader,
  HostingGate,
  Spinner,
  ToneBadge,
  primaryButton,
} from '../components/containers/ContainerBits';

const LIST_POLL_MS = 5000;

function ContainerRow({ projectId, app }: { readonly projectId: string; readonly app: App }) {
  const { data: deploys, isLoading } = useDeploys(projectId, app.id, LIST_POLL_MS, 1);
  const last = deploys?.[0];
  const status = appDisplayStatus(app, last);

  return (
    <div
      className="flex items-center justify-between gap-4 px-4 py-3 bg-surface-card border border-border-primary rounded-lg"
      data-testid={`container-row-${app.id}`}
    >
      <div className="min-w-0 space-y-0.5">
        <Link
          to={`/project/${projectId}/containers/${app.id}`}
          className="text-sm font-medium text-text-primary hover:text-purple-400"
        >
          {app.name}
        </Link>
        <p className="text-xs font-mono text-text-tertiary truncate">{app.image}</p>
        {app.url && (
          <a
            href={app.url}
            target="_blank"
            rel="noreferrer"
            className="text-xs text-purple-400 hover:underline"
          >
            {app.url}
          </a>
        )}
      </div>
      <div className="flex items-center gap-4 flex-shrink-0">
        <span
          className="text-xs text-text-secondary"
          data-testid={`container-last-deploy-${app.id}`}
        >
          {last
            ? `Revision ${last.revision} · ${formatWhen(last.createdAt)}`
            : !isLoading && 'Never deployed'}
        </span>
        {!isLoading && <ToneBadge {...status} testId={`container-status-${app.id}`} />}
      </div>
    </div>
  );
}

function EmptyState({ projectId }: { readonly projectId: string }) {
  return (
    <div
      className="bg-surface-card border border-border-primary rounded-lg p-8 text-center"
      data-testid="containers-empty"
    >
      <p className="text-sm text-text-primary font-medium">No containers yet</p>
      <p className="text-sm text-text-secondary mt-2 max-w-lg mx-auto">
        A container runs your own container image beside this project, on a public URL. It uses the
        size your project's plan includes, so there is nothing extra to choose. You only need an
        image and the port it listens on.
      </p>
      <Link to={`/project/${projectId}/containers/new`} className={`${primaryButton} mt-5`}>
        <Plus className="w-4 h-4" /> New container
      </Link>
    </div>
  );
}

function ContainersList({ projectId }: { readonly projectId: string }) {
  const { data: apps, isLoading, error } = useApps(projectId);

  if (isLoading) return <Spinner />;
  if (error) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load the containers')}
      </div>
    );
  }
  if (!apps || apps.length === 0) return <EmptyState projectId={projectId} />;
  return (
    <div className="space-y-2" data-testid="containers-list">
      {apps.map((app) => (
        <ContainerRow key={app.id} projectId={projectId} app={app} />
      ))}
    </div>
  );
}

export function ContainersPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { enabled } = useAppHostingEnabled();
  return (
    <div data-testid="containers-page">
      <ContainersHeader
        title="Containers"
        subtitle="Run your own container image next to this project's database."
        actions={
          enabled && (
            <Link
              to={`/project/${projectId}/containers/new`}
              className={primaryButton}
              data-testid="containers-new"
            >
              <Plus className="w-4 h-4" /> New
            </Link>
          )
        }
      />
      <HostingGate>
        <ContainersList projectId={projectId} />
      </HostingGate>
    </div>
  );
}
