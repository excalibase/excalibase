import { Link, useParams } from 'react-router-dom';
import { Pencil, Rocket } from 'lucide-react';
import {
  apiErrorMessage,
  isDeployInProgress,
  useApp,
  useDeployApp,
  useDeploys,
  useRedeployApp,
  type App,
  type Deploy,
  type EnvVar,
} from '../api/apps';
import {
  DEPLOY_STATUS,
  appDisplayStatus,
  describeTier,
  formatWhen,
  plainFailureReason,
} from '../components/containers/appCopy';
import { DeployHistory } from '../components/containers/DeployHistory';
import {
  ContainersHeader,
  HostingGate,
  Spinner,
  ToneBadge,
  primaryButton,
  secondaryButton,
} from '../components/containers/ContainerBits';

const DEFAULT_POLL_MS = 2000;
const MASK = '••••••••';

function CurrentDeploy({ deploy }: { readonly deploy?: Deploy }) {
  if (!deploy) return null;
  const status = DEPLOY_STATUS[deploy.status];
  return (
    <div
      className="bg-surface-card border border-border-primary rounded-lg p-4 mb-4"
      data-testid="current-deploy"
    >
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-text-primary">
          Revision {deploy.revision} ·{' '}
          <span className="font-mono text-text-secondary">{deploy.image}</span>
        </p>
        <span data-testid="current-deploy-status">
          <ToneBadge label={status.label} tone={status.tone} />
        </span>
      </div>
      {deploy.status === 'failed' && (
        <div className="mt-3 space-y-1" data-testid="current-deploy-reason">
          <p className="text-sm text-red-400">{plainFailureReason(deploy.failureReason)}</p>
          {deploy.failureReason && (
            <p className="text-xs font-mono text-text-tertiary break-all">
              Details: {deploy.failureReason}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function describeVar(v: EnvVar): string {
  if (v.kind === 'reference' && v.reference) {
    return `Linked to database ${v.reference.sourceName} (${v.reference.variable})`;
  }
  if (v.kind === 'secret') return `${MASK} secret, set`;
  return MASK;
}

function Settings({ app }: { readonly app: App }) {
  const size = describeTier(app.tier);
  const rows: Array<[string, string]> = [
    ['Port', String(app.port)],
    ['Copies', app.replicas === 0 ? '0 (stopped)' : String(app.replicas)],
    ['Size', `${size.label}: ${size.detail}`],
    ['Health check', app.healthCheckPath || 'None'],
  ];
  return (
    <dl className="bg-surface-card border border-border-primary rounded-lg divide-y divide-border-primary">
      {rows.map(([label, value]) => (
        <div key={label} className="flex gap-4 px-4 py-2.5 text-sm">
          <dt className="w-32 text-text-tertiary flex-shrink-0">{label}</dt>
          <dd className="text-text-primary">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Variables({ env }: { readonly env: EnvVar[] }) {
  if (env.length === 0) return <p className="text-sm text-text-tertiary">No variables.</p>;
  return (
    <ul className="bg-surface-card border border-border-primary rounded-lg divide-y divide-border-primary">
      {env.map((v) => (
        <li
          key={v.name}
          className="flex gap-4 px-4 py-2.5 text-sm"
          data-testid={`env-view-${v.name}`}
        >
          <span className="w-48 font-mono text-text-primary truncate flex-shrink-0">{v.name}</span>
          <span className="text-text-secondary">{describeVar(v)}</span>
        </li>
      ))}
    </ul>
  );
}

function Detail({
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
  const deployApp = useDeployApp(projectId, appId);
  const redeploy = useRedeployApp(projectId, appId);

  if (isLoading) return <Spinner />;
  if (error || !app) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load this container')}
      </div>
    );
  }

  const newest = deploys[0];
  const busy = deployApp.isPending || (newest !== undefined && isDeployInProgress(newest.status));
  const status = appDisplayStatus(app, newest);
  const actionError = deployApp.error ?? redeploy.error;

  return (
    <>
      <ContainersHeader
        title={app.name}
        subtitle={app.image}
        actions={
          <>
            <ToneBadge {...status} testId="container-status" />
            <Link
              to={`/project/${projectId}/containers/${appId}/edit`}
              className={secondaryButton}
              data-testid="edit-button"
            >
              <Pencil className="w-4 h-4" /> Edit
            </Link>
            <button
              type="button"
              onClick={() => deployApp.mutate()}
              disabled={busy}
              className={primaryButton}
              data-testid="deploy-button"
            >
              <Rocket className="w-4 h-4" /> Deploy
            </button>
          </>
        }
      />
      {app.url && (
        <p className="mb-4 text-sm">
          <a
            href={app.url}
            target="_blank"
            rel="noreferrer"
            className="text-purple-400 hover:underline"
          >
            {app.url}
          </a>
        </p>
      )}
      {actionError && (
        <div
          role="alert"
          className="mb-4 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
        >
          {apiErrorMessage(actionError, 'The deploy could not be started')}
        </div>
      )}
      <CurrentDeploy deploy={newest} />
      <div className="grid gap-6 lg:grid-cols-2 mb-6">
        <section className="space-y-2">
          <h4 className="text-sm font-semibold text-text-primary">Settings</h4>
          <Settings app={app} />
        </section>
        <section className="space-y-2">
          <h4 className="text-sm font-semibold text-text-primary">Variables</h4>
          <Variables env={app.env} />
        </section>
      </div>
      <section className="space-y-2">
        <h4 className="text-sm font-semibold text-text-primary">Deployments</h4>
        <div className="bg-surface-card border border-border-primary rounded-lg">
          <DeployHistory
            deploys={deploys}
            redeploying={redeploy.isPending}
            onRedeploy={(id) => redeploy.mutate(id)}
          />
        </div>
        {newest?.finishedAt && (
          <p className="text-xs text-text-tertiary">
            Last finished {formatWhen(newest.finishedAt)}
          </p>
        )}
      </section>
    </>
  );
}

export function ContainerDetailPage({
  pollIntervalMs = DEFAULT_POLL_MS,
}: {
  readonly pollIntervalMs?: number;
}) {
  const { projectId = '', appId = '' } = useParams<{ projectId: string; appId: string }>();
  return (
    <div data-testid="container-detail-page">
      <Link
        to={`/project/${projectId}/containers`}
        className="text-xs text-text-tertiary hover:text-text-primary"
      >
        ← Containers
      </Link>
      <div className="mt-3">
        <HostingGate>
          <Detail projectId={projectId} appId={appId} pollIntervalMs={pollIntervalMs} />
        </HostingGate>
      </div>
    </div>
  );
}
