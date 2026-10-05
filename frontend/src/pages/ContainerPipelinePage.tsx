import { Link, useParams } from 'react-router-dom';
import {
  apiErrorMessage,
  isDeployInProgress,
  useApp,
  useAppFollowsDeploys,
  useDeploys,
  useRedeployApp,
} from '../api/apps';
import { AppLogs } from '../components/containers/AppLogs';
import { AutoDeployCard } from '../components/containers/AutoDeployCard';
import { CiSetup } from '../components/containers/CiSetup';
import { ContainersHeader, HostingGate, Spinner } from '../components/containers/ContainerBits';
import { PipelineDeploys } from '../components/containers/PipelineDeploys';

const DEFAULT_POLL_MS = 2000;

function Pipeline({
  projectId,
  appId,
  pollIntervalMs,
}: {
  readonly projectId: string;
  readonly appId: string;
  readonly pollIntervalMs: number;
}) {
  const { data: app, isLoading, error } = useApp(projectId, appId);
  const { data: deploys = [] } = useDeploys(projectId, appId, pollIntervalMs);
  const redeploy = useRedeployApp(projectId, appId);
  useAppFollowsDeploys(projectId, appId, deploys[0]);

  if (isLoading) return <Spinner />;
  if (error || !app) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load this container')}
      </div>
    );
  }
  const newest = deploys[0];
  const busy = redeploy.isPending || (newest !== undefined && isDeployInProgress(newest.status));

  return (
    <>
      <ContainersHeader title={`${app.name} pipeline`} subtitle={app.image} />
      {redeploy.error && (
        <div
          role="alert"
          className="mb-4 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
        >
          {apiErrorMessage(redeploy.error, 'The deploy could not be started')}
        </div>
      )}
      <section className="space-y-2 mb-6">
        <h4 className="text-sm font-semibold text-text-primary">Deploys</h4>
        <div className="bg-surface-card border border-border-primary rounded-lg">
          <PipelineDeploys deploys={deploys} busy={busy} onRedeploy={(id) => redeploy.mutate(id)} />
        </div>
      </section>
      <section className="space-y-2 mb-6">
        <h4 className="text-sm font-semibold text-text-primary">Auto-deploy</h4>
        <AutoDeployCard app={app} />
      </section>
      <section className="space-y-2 mb-6">
        <h4 className="text-sm font-semibold text-text-primary">Deploy from CI</h4>
        <CiSetup projectId={projectId} appId={appId} image={app.image} />
      </section>
      <AppLogs projectId={projectId} appId={appId} />
    </>
  );
}

export function ContainerPipelinePage({
  pollIntervalMs = DEFAULT_POLL_MS,
}: {
  readonly pollIntervalMs?: number;
}) {
  const { projectId = '', appId = '' } = useParams<{ projectId: string; appId: string }>();
  return (
    <div data-testid="container-pipeline-page">
      <Link
        to={`/project/${projectId}/containers/${appId}`}
        className="text-xs text-text-tertiary hover:text-text-primary"
      >
        ← Container
      </Link>
      <div className="mt-3">
        <HostingGate>
          <Pipeline projectId={projectId} appId={appId} pollIntervalMs={pollIntervalMs} />
        </HostingGate>
      </div>
    </div>
  );
}
