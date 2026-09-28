import { Link } from 'react-router-dom';
import { Container, Link2, Loader2, Plus } from 'lucide-react';
import { useApps, useDeploys, apiErrorMessage, type App } from '../../api/apps';
import { appDisplayStatus } from '../containers/appCopy';
import { ToneBadge, primaryButton, secondaryButton } from '../containers/ContainerBits';
import { containersSummary } from './serviceStatus';
import { AccessNote, ServiceCard } from './ServiceCard';

const POLL_MS = 5000;
const SHOWN = 5;

const usesDatabase = (app: App): boolean =>
  app.env.some(
    (variable) => variable.kind === 'reference' && variable.reference?.sourceKind === 'database',
  );

function AppRow({ projectId, app }: { readonly projectId: string; readonly app: App }) {
  const { data: deploys, isLoading } = useDeploys(projectId, app.id, POLL_MS, 1);
  return (
    <li className="flex items-center justify-between gap-3 py-2">
      <div className="min-w-0">
        <Link
          to={`/project/${projectId}/containers/${app.id}`}
          className="text-sm font-medium text-text-primary hover:text-purple-400"
        >
          {app.name}
        </Link>
        {usesDatabase(app) && (
          <p
            className="flex items-center gap-1 text-xs text-text-tertiary"
            data-testid={`service-app-uses-database-${app.id}`}
          >
            <Link2 className="w-3 h-3" /> Uses the database
          </p>
        )}
      </div>
      {!isLoading && (
        <ToneBadge
          {...appDisplayStatus(app, deploys?.[0])}
          testId={`service-app-status-${app.id}`}
        />
      )}
    </li>
  );
}

function EmptyContainers({ projectId }: { readonly projectId: string }) {
  return (
    <div data-testid="service-containers-empty" className="text-sm text-text-secondary space-y-3">
      <p>
        A container runs your own container image on a public URL, beside this project. It uses the
        size your project&apos;s plan includes, so there is nothing extra to choose or pay for. You
        only need an image and the port it listens on.
      </p>
      <Link to={`/project/${projectId}/containers/new`} className={primaryButton}>
        <Plus className="w-4 h-4" /> New container
      </Link>
    </div>
  );
}

function ContainersBody({ projectId }: { readonly projectId: string }) {
  const { data: apps, error } = useApps(projectId);
  // A paused retry is neither loading nor failed; only a real answer may
  // say there are no containers.
  if (error) {
    return (
      <p role="alert" className="text-sm text-red-400">
        {apiErrorMessage(error, 'Could not load the containers')}
      </p>
    );
  }
  if (!apps) return <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />;
  const list = apps;
  return (
    <>
      <p className="text-sm text-text-primary" data-testid="service-containers-summary">
        {containersSummary(list)}
      </p>
      {list.length === 0 ? (
        <EmptyContainers projectId={projectId} />
      ) : (
        <>
          <ul className="divide-y divide-border-primary">
            {list.slice(0, SHOWN).map((app) => (
              <AppRow key={app.id} projectId={projectId} app={app} />
            ))}
          </ul>
          <div>
            <Link to={`/project/${projectId}/containers`} className={secondaryButton}>
              Open containers
            </Link>
          </div>
        </>
      )}
    </>
  );
}

// Rendered only when the server has app hosting on; the caller decides.
export function ContainersServiceCard({ projectId }: { readonly projectId: string }) {
  return (
    <ServiceCard
      testId="service-containers"
      icon={Container}
      title="Containers"
      description="Your own container images, run beside the project."
    >
      <ContainersBody projectId={projectId} />
      <AccessNote testId="service-containers-access">
        Public by default: each container is a web service on its own URL, and your app decides who
        may use it. A container reaches the database privately, inside the project, only through
        variables you link to it.
      </AccessNote>
    </ServiceCard>
  );
}
