import { useState } from 'react';
import { GitBranch, RefreshCw } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import {
  EMPTY_SETTINGS,
  startGitSignIn,
  useConnectRepository,
  useDisconnectRepository,
  useGitRepositories,
  useGitState,
  useRebuildSite,
  type GitRepository,
  type SiteBuildSettings,
  type SiteGitConnection,
} from '../../api/siteBuilds';
import { useSiteGitBuildsEnabled } from '../../hooks/useDeploymentMode';
import { Spinner, primaryButton, secondaryButton } from '../containers/ContainerBits';
import { SiteBuildList } from './SiteBuildList';
import { SiteBuildSecrets } from './SiteBuildSecrets';
import { SiteBuildSettingsForm } from './SiteBuildSettingsForm';
import { SitePreviews } from './SitePreviews';

interface SiteProps {
  readonly projectId: string;
  readonly siteId: string;
  readonly canDevelop: boolean;
  readonly canAdmin?: boolean;
}

const card = 'bg-surface-card border border-border-primary rounded-lg p-4 space-y-3';

function SignInLinks({
  projectId,
  siteId,
}: {
  readonly projectId: string;
  readonly siteId: string;
}) {
  const [error, setError] = useState<unknown>(null);
  const go = (install: boolean) =>
    startGitSignIn(projectId, siteId, install).then((url) => window.location.assign(url), setError);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        type="button"
        className={primaryButton}
        onClick={() => go(false)}
        data-testid="git-sign-in"
      >
        Sign in with GitHub
      </button>
      <button
        type="button"
        className={secondaryButton}
        onClick={() => go(true)}
        data-testid="git-install"
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
  siteId,
  current,
  nodeVersions,
  onDone,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly current: SiteGitConnection | null;
  readonly nodeVersions: string[];
  readonly onDone?: () => void;
}) {
  const { data: access, error, isLoading } = useGitRepositories(projectId, siteId, true);
  const connect = useConnectRepository(projectId, siteId);
  const [repositoryId, setRepositoryId] = useState<number | null>(current?.repositoryId ?? null);
  const [settings, setSettings] = useState<SiteBuildSettings>(current?.settings ?? EMPTY_SETTINGS);
  const repositories: GitRepository[] = (access ?? []).flatMap((entry) => entry.repositories);
  const submit = () => {
    if (repositoryId === null) return;
    connect.mutate({ repositoryId, settings }, { onSuccess: () => onDone?.() });
  };
  if (isLoading) return <Spinner />;
  return (
    <div className="space-y-3" data-testid="git-connect">
      {(error || repositories.length === 0) && (
        <p className="text-sm text-text-secondary">
          Sign in with GitHub so Studio sees the repositories you can read. If yours is not listed,
          install the GitHub App on it first.
        </p>
      )}
      <SignInLinks projectId={projectId} siteId={siteId} />
      {repositories.length > 0 && (
        <>
          <select
            className="w-full rounded-md border border-border-primary bg-surface-primary px-3 py-1.5 text-sm text-text-primary"
            value={repositoryId ?? ''}
            onChange={(event) => setRepositoryId(Number(event.target.value) || null)}
            data-testid="git-repository"
          >
            <option value="">Choose a repository</option>
            {repositories.map((repository) => (
              <option key={repository.id} value={repository.id}>
                {repository.fullName}
              </option>
            ))}
          </select>
          <SiteBuildSettingsForm
            settings={settings}
            nodeVersions={nodeVersions}
            onChange={setSettings}
          />
          <button
            type="button"
            className={primaryButton}
            disabled={repositoryId === null || connect.isPending}
            onClick={submit}
            data-testid="git-connect-submit"
          >
            {current ? 'Save and rebuild' : 'Connect and build'}
          </button>
        </>
      )}
      {connect.data?.buildError && (
        <p className="text-xs text-amber-400" data-testid="git-build-error">
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
  siteId,
  canDevelop,
  connection,
  nodeVersions,
}: SiteProps & {
  readonly connection: SiteGitConnection;
  readonly nodeVersions: string[];
}) {
  const rebuild = useRebuildSite(projectId, siteId);
  const disconnect = useDisconnectRepository(projectId, siteId);
  const [editing, setEditing] = useState(false);
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-text-primary" data-testid="git-connected">
          <GitBranch className="inline w-4 h-4 mr-1" />
          {connection.repository} · {connection.settings.branch}
          {connection.settings.rootDir ? ` · ${connection.settings.rootDir}` : ''}
          <span className="block text-xs text-text-tertiary" data-testid="git-connected-by">
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
              data-testid="git-rebuild"
            >
              <RefreshCw className="w-4 h-4" /> Rebuild
            </button>
            <button
              type="button"
              className={secondaryButton}
              onClick={() => setEditing(!editing)}
              data-testid="git-edit"
            >
              {editing ? 'Close' : 'Settings'}
            </button>
            <button
              type="button"
              className={secondaryButton}
              disabled={disconnect.isPending}
              onClick={() => disconnect.mutate()}
              data-testid="git-disconnect"
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
        <div className="space-y-2" data-testid="git-inactive">
          <p className="text-sm text-amber-400">
            Builds are stopped: {connection.inactiveReason}. A developer signs in with GitHub and
            connects the repository again.
          </p>
          {canDevelop && (
            <Connect
              projectId={projectId}
              siteId={siteId}
              current={connection}
              nodeVersions={nodeVersions}
            />
          )}
        </div>
      )}
      {editing && (
        <Connect
          projectId={projectId}
          siteId={siteId}
          current={connection}
          nodeVersions={nodeVersions}
          onDone={() => setEditing(false)}
        />
      )}
    </div>
  );
}

// Deploy on git push (EXC-354): hidden until the server switches it on; single-host answers why it has none.
export function SiteGitBuilds({ projectId, siteId, canDevelop, canAdmin = false }: SiteProps) {
  const enabled = useSiteGitBuildsEnabled();
  const { data: state, error } = useGitState(projectId, siteId, enabled);
  if (!enabled) return null;
  return (
    <section className="space-y-2 mb-6" data-testid="site-git-builds">
      <h4 className="text-sm font-semibold text-text-primary">Deploy on git push</h4>
      <div className={card}>
        {error && (
          <p className="text-sm text-text-secondary" data-testid="git-unavailable">
            {apiErrorMessage(error, 'Git builds are not available on this install')}
          </p>
        )}
        {!error && !state && <Spinner />}
        {state?.connection && (
          <Connected
            projectId={projectId}
            siteId={siteId}
            canDevelop={canDevelop}
            connection={state.connection}
            nodeVersions={state.nodeVersions}
          />
        )}
        {state && !state.connection && canDevelop && (
          <Connect
            projectId={projectId}
            siteId={siteId}
            current={null}
            nodeVersions={state.nodeVersions}
          />
        )}
        {state && !state.connection && !canDevelop && (
          <p className="text-sm text-text-tertiary">No repository is connected.</p>
        )}
      </div>
      {state?.connection && (
        <div className="bg-surface-card border border-border-primary rounded-lg">
          <SiteBuildList projectId={projectId} siteId={siteId} canReadLogs={canDevelop} />
        </div>
      )}
      {state?.connection && (
        <SitePreviews
          projectId={projectId}
          siteId={siteId}
          productionBranch={state.connection.settings.branch}
          canDevelop={canDevelop}
          canAdmin={canAdmin}
        />
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
