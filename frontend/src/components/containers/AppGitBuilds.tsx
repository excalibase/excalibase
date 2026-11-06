import { useState } from 'react';
import { GitBranch, RefreshCw } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import {
  EMPTY_APP_SETTINGS,
  startAppGitSignIn,
  useAppGitRepositories,
  useAppGitState,
  useConnectAppRepository,
  useDisconnectAppRepository,
  useRebuildApp,
  type AppBuilder,
  type AppBuildSettings,
  type AppGitConnection,
} from '../../api/appBuilds';
import { useFeatureEnabled } from '../../hooks/useDeploymentMode';
import { SiteBuildSecrets } from '../sites/SiteBuildSecrets';
import { AppBuildList } from './AppBuildList';
import { AppBuildSettingsForm } from './AppBuildSettingsForm';
import { Spinner, primaryButton, secondaryButton } from './ContainerBits';

interface AppProps {
  readonly projectId: string;
  readonly appId: string;
  readonly canDevelop: boolean;
}

const card = 'bg-surface-card border border-border-primary rounded-lg p-4 space-y-3';

function SignInLinks({ projectId, appId }: Pick<AppProps, 'projectId' | 'appId'>) {
  const [error, setError] = useState<unknown>(null);
  const go = (install: boolean) =>
    startAppGitSignIn(projectId, appId, install).then(
      (url) => window.location.assign(url),
      setError,
    );
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        type="button"
        className={primaryButton}
        onClick={() => go(false)}
        data-testid="app-git-sign-in"
      >
        Sign in with GitHub
      </button>
      <button
        type="button"
        className={secondaryButton}
        onClick={() => go(true)}
        data-testid="app-git-install"
      >
        Install the GitHub App on a repository
      </button>
      {error !== null && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(error, 'GitHub could not be reached')}
        </p>
      )}
    </div>
  );
}

function Connect({
  projectId,
  appId,
  current,
  builders,
  onDone,
}: Pick<AppProps, 'projectId' | 'appId'> & {
  readonly current: AppGitConnection | null;
  readonly builders: AppBuilder[];
  readonly onDone?: () => void;
}) {
  const { data: access, error, isLoading } = useAppGitRepositories(projectId, appId, true);
  const connect = useConnectAppRepository(projectId, appId);
  const [repositoryId, setRepositoryId] = useState<number | null>(current?.repositoryId ?? null);
  const [settings, setSettings] = useState<AppBuildSettings>(
    current?.settings ?? EMPTY_APP_SETTINGS,
  );
  const repositories = (access ?? []).flatMap((entry) => entry.repositories);
  const submit = () => {
    if (repositoryId === null) return;
    connect.mutate({ repositoryId, settings }, { onSuccess: () => onDone?.() });
  };
  if (isLoading) return <Spinner />;
  return (
    <div className="space-y-3" data-testid="app-git-connect">
      {(error || repositories.length === 0) && (
        <p className="text-sm text-text-secondary">
          Sign in with GitHub so Studio sees the repositories you can read. If yours is not listed,
          install the GitHub App on it first.
        </p>
      )}
      <SignInLinks projectId={projectId} appId={appId} />
      {repositories.length > 0 && (
        <>
          <select
            className="w-full rounded-md border border-border-primary bg-surface-primary px-3 py-1.5 text-sm text-text-primary"
            value={repositoryId ?? ''}
            onChange={(event) => setRepositoryId(Number(event.target.value) || null)}
            data-testid="app-git-repository"
          >
            <option value="">Choose a repository</option>
            {repositories.map((repository) => (
              <option key={repository.id} value={repository.id}>
                {repository.fullName}
              </option>
            ))}
          </select>
          <AppBuildSettingsForm settings={settings} builders={builders} onChange={setSettings} />
          <button
            type="button"
            className={primaryButton}
            disabled={repositoryId === null || connect.isPending}
            onClick={submit}
            data-testid="app-git-connect-submit"
          >
            {current ? 'Save and rebuild' : 'Connect and build'}
          </button>
        </>
      )}
      {connect.data?.buildError && (
        <p className="text-xs text-amber-400" data-testid="app-git-build-error">
          Connected, but no build was started: {connect.data.buildError}
        </p>
      )}
      {connect.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(connect.error, 'The repository could not be connected')}
        </p>
      )}
    </div>
  );
}

function Connected({
  projectId,
  appId,
  canDevelop,
  connection,
  builders,
}: AppProps & { readonly connection: AppGitConnection; readonly builders: AppBuilder[] }) {
  const rebuild = useRebuildApp(projectId, appId);
  const disconnect = useDisconnectAppRepository(projectId, appId);
  const [editing, setEditing] = useState(false);
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-text-primary" data-testid="app-git-connected">
          <GitBranch className="inline w-4 h-4 mr-1" />
          {connection.repository} · {connection.settings.branch}
          {connection.settings.rootDir ? ` · ${connection.settings.rootDir}` : ''}
          <span className="block text-xs text-text-tertiary" data-testid="app-git-connected-by">
            Connected by {connection.connectedByName || connection.connectedBy}
          </span>
        </p>
        {canDevelop && !connection.inactiveReason && (
          <div className="flex items-center gap-2">
            <button
              type="button"
              className={secondaryButton}
              disabled={rebuild.isPending}
              onClick={() => rebuild.mutate()}
              data-testid="app-git-rebuild"
            >
              <RefreshCw className="w-4 h-4" /> Rebuild
            </button>
            <button
              type="button"
              className={secondaryButton}
              onClick={() => setEditing(!editing)}
              data-testid="app-git-edit"
            >
              {editing ? 'Close' : 'Settings'}
            </button>
            <button
              type="button"
              className={secondaryButton}
              disabled={disconnect.isPending}
              onClick={() => disconnect.mutate()}
              data-testid="app-git-disconnect"
            >
              Disconnect
            </button>
          </div>
        )}
      </div>
      {rebuild.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(rebuild.error, 'The rebuild could not be started')}
        </p>
      )}
      {connection.inactiveReason && (
        <div className="space-y-2" data-testid="app-git-inactive">
          <p className="text-sm text-amber-400">
            Builds are stopped: {connection.inactiveReason}. A developer signs in with GitHub and
            connects the repository again.
          </p>
          {canDevelop && (
            <Connect projectId={projectId} appId={appId} current={connection} builders={builders} />
          )}
        </div>
      )}
      {editing && (
        <Connect
          projectId={projectId}
          appId={appId}
          current={connection}
          builders={builders}
          onDone={() => setEditing(false)}
        />
      )}
    </div>
  );
}

// Deploy an app on git push (EXC-547): hidden until the server switches appgitbuilds on.
export function AppGitBuilds({ projectId, appId, canDevelop }: AppProps) {
  const { enabled } = useFeatureEnabled('appgitbuilds');
  const { data: state, error } = useAppGitState(projectId, appId, enabled);
  if (!enabled) return null;
  return (
    <section className="space-y-2 mb-6" data-testid="app-git-builds">
      <h4 className="text-sm font-semibold text-text-primary">Deploy on git push</h4>
      <div className={card}>
        {error && (
          <p className="text-sm text-text-secondary" data-testid="app-git-unavailable">
            {apiErrorMessage(error, 'Git builds are not available on this install')}
          </p>
        )}
        {!error && !state && <Spinner />}
        {state?.connection && (
          <Connected
            projectId={projectId}
            appId={appId}
            canDevelop={canDevelop}
            connection={state.connection}
            builders={state.builders}
          />
        )}
        {state && !state.connection && canDevelop && (
          <Connect projectId={projectId} appId={appId} current={null} builders={state.builders} />
        )}
        {state && !state.connection && !canDevelop && (
          <p className="text-sm text-text-tertiary">No repository is connected.</p>
        )}
      </div>
      {state?.connection && (
        <div className="bg-surface-card border border-border-primary rounded-lg">
          <AppBuildList projectId={projectId} appId={appId} canReadLogs={canDevelop} />
        </div>
      )}
      {state && canDevelop && (
        <details className={card}>
          <summary className="cursor-pointer text-sm text-text-primary">
            Project secrets in builds
          </summary>
          <SiteBuildSecrets projectId={projectId} />
        </details>
      )}
    </section>
  );
}
