import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Pencil, Rocket, Workflow } from 'lucide-react';
import { apiErrorMessage, isDeployInProgress, useApp, useDeployApp, useDeploys, useRedeployApp, type App, type Deploy } from '../../../api/apps';
import { useCustomDomainsEnabled, useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { useProjectRole } from '../../../hooks/useProjectRole';
import { CurrentDeploy, DiskStopBanner, RestartsBanner, Settings, Variables } from '../../../pages/ContainerDetailPage';
import { AppDiskCard } from '../../containers/AppDiskCard';
import { AppDomains, HostCertificateBadge } from '../../containers/AppDomains';
import { AppGitBuilds } from '../../containers/AppGitBuilds';
import { AppLogs } from '../../containers/AppLogs';
import { DeployHistory } from '../../containers/DeployHistory';
import { LifecycleActions } from '../../containers/LifecycleActions';
import { Spinner, primaryButton, secondaryButton } from '../../containers/ContainerBits';
import { diskStopReason, formatWhen } from '../../containers/appCopy';
import { ProjectContainersSection } from '../../monitoring/ProjectContainersSection';
import { appCardStatus } from './cardStatus';
import { PanelFrame } from './PanelFrame';
import { tabsFor } from './panelState';

const POLL_MS = 2000;

interface AppProps {
  readonly projectId: string;
  readonly app: App;
}

interface DeploysProps extends AppProps {
  readonly deploys: readonly Deploy[];
}

function Overview({ projectId, app, deploys }: DeploysProps) {
  const deployApp = useDeployApp(projectId, app.id);
  const [lifecycleError, setLifecycleError] = useState<unknown>(null);
  const customDomains = useCustomDomainsEnabled();
  const { enabled: pipeline } = useFeatureEnabled('pipeline');
  const newest = deploys[0];
  const busy = deployApp.isPending || (newest !== undefined && isDeployInProgress(newest.status));
  const actionError = deployApp.error ?? lifecycleError;
  return (
    <div className="space-y-4" data-testid="app-overview">
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" onClick={() => deployApp.mutate()} disabled={busy} className={primaryButton}>
          <Rocket className="h-4 w-4" aria-hidden="true" /> Deploy
        </button>
        <LifecycleActions app={app} deployed={newest !== undefined} onError={setLifecycleError} followMs={POLL_MS} />
        {pipeline && (
          <Link to={`/project/${projectId}/containers/${app.id}/pipeline`} className={secondaryButton}>
            <Workflow className="h-4 w-4" aria-hidden="true" /> Pipeline
          </Link>
        )}
      </div>
      {app.url && (
        <p className="flex flex-wrap items-center gap-2 text-sm">
          <a href={app.url} target="_blank" rel="noreferrer" className="text-purple-400 hover:underline">{app.url}</a>
          {customDomains && <HostCertificateBadge projectId={projectId} appId={app.id} />}
        </p>
      )}
      {actionError != null && (
        <div role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400">
          {apiErrorMessage(actionError, lifecycleError ? 'The app could not be changed' : 'The deploy could not be started')}
        </div>
      )}
      <DiskStopBanner reason={diskStopReason(app, newest)} />
      <RestartsBanner app={app} />
      <CurrentDeploy deploy={newest} />
      {newest?.finishedAt && <p className="text-xs text-text-tertiary">Last finished {formatWhen(newest.finishedAt)}</p>}
    </div>
  );
}

function Deploys({ projectId, app, deploys }: DeploysProps) {
  const redeploy = useRedeployApp(projectId, app.id);
  const { canDevelop } = useProjectRole(projectId);
  return (
    <div className="space-y-6">
      <AppGitBuilds projectId={projectId} appId={app.id} canDevelop={canDevelop} />
      <section className="space-y-2">
        <h3 className="text-sm font-semibold text-text-primary">Deployments</h3>
        <div className="rounded-lg border border-border-primary bg-bg-secondary">
          <DeployHistory deploys={[...deploys]} redeploying={redeploy.isPending} onRedeploy={(id) => redeploy.mutate(id)} />
        </div>
      </section>
    </div>
  );
}

function VariablesTab({ projectId, app }: AppProps) {
  return (
    <div className="space-y-3">
      <Variables env={app.env} />
      <Link to={`/project/${projectId}/containers/${app.id}/edit`} className={secondaryButton}>
        <Pencil className="h-4 w-4" aria-hidden="true" /> Edit variables
      </Link>
    </div>
  );
}

function MetricsTab({ projectId }: Pick<AppProps, 'projectId'>) {
  const { enabled } = useFeatureEnabled('hostmetrics');
  if (enabled) return <ProjectContainersSection projectId={projectId} />;
  return (
    <p className="text-sm text-text-secondary">
      Per-app metrics are not available on this installation. The project&apos;s resource use is on the{' '}
      <Link to={`/project/${projectId}/monitoring/metrics`} className="text-purple-400 hover:underline">Metrics</Link> tab.
    </p>
  );
}

function SettingsTab({ projectId, app }: AppProps) {
  const customDomains = useCustomDomainsEnabled();
  return (
    <div className="space-y-6">
      <Settings app={app} />
      {app.disk && <AppDiskCard app={{ ...app, disk: app.disk }} />}
      {customDomains && <AppDomains projectId={projectId} appId={app.id} />}
      <Link to={`/project/${projectId}/containers/${app.id}/edit`} className={secondaryButton}>
        <Pencil className="h-4 w-4" aria-hidden="true" /> Edit app
      </Link>
    </div>
  );
}

function TabBody({ tab, projectId, app, deploys }: DeploysProps & { readonly tab: string }) {
  switch (tab) {
    case 'deploys':
      return <Deploys projectId={projectId} app={app} deploys={deploys} />;
    case 'variables':
      return <VariablesTab projectId={projectId} app={app} />;
    case 'metrics':
      return <MetricsTab projectId={projectId} />;
    case 'logs':
      return <AppLogs projectId={projectId} appId={app.id} />;
    case 'settings':
      return <SettingsTab projectId={projectId} app={app} />;
    default:
      return <Overview projectId={projectId} app={app} deploys={deploys} />;
  }
}

interface AppPanelProps {
  readonly projectId: string;
  readonly appId: string;
  readonly tab: string;
  readonly onTab: (key: string) => void;
  readonly onClose: () => void;
}

export function AppPanel({ projectId, appId, tab, onTab, onClose }: AppPanelProps) {
  const { data: app, error } = useApp(projectId, appId);
  const { data: deploys = [] } = useDeploys(projectId, appId, POLL_MS);
  if (error || !app) {
    return (
      <PanelFrame title="App" onClose={onClose} idPrefix="app">
        {error ? <p role="alert" className="text-sm text-red-400">{apiErrorMessage(error, 'Could not load this app')}</p> : <Spinner />}
      </PanelFrame>
    );
  }
  return (
    <PanelFrame
      title={app.name}
      subtitle={app.image}
      status={appCardStatus(app, deploys)}
      tabsLabel="App"
      tabs={tabsFor('app')}
      tab={tab}
      onTab={onTab}
      onClose={onClose}
      idPrefix="app"
    >
      <TabBody projectId={projectId} app={app} deploys={deploys} tab={tab} />
    </PanelFrame>
  );
}
