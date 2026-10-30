import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { apiErrorMessage } from '../api/apps';
import {
  useDeleteSite,
  useDeploySite,
  useRollbackSite,
  useSetSpaFallback,
  useSite,
  useSiteDeploys,
  type Site,
} from '../api/sites';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import { Spinner, ToneBadge, secondaryButton } from '../components/containers/ContainerBits';
import { SitesGate, SitesHeader } from '../components/sites/SiteBits';
import { SiteDeployHistory } from '../components/sites/SiteDeployHistory';
import { SiteUpload } from '../components/sites/SiteUpload';
import { SiteCiSnippet } from '../components/sites/SiteCiSnippet';
import { SiteDeployTokens } from '../components/sites/SiteDeployTokens';
import { SiteDomains } from '../components/sites/SiteDomains';
import { SiteGitBuilds } from '../components/sites/SiteGitBuilds';
import { useProjectRole } from '../hooks/useProjectRole';

// What a caller who may not change the site sees of its routing.
function RoutingState({ site }: { readonly site: Site }) {
  return (
    <section className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-2">
      <p className="text-sm text-text-primary" data-testid="site-spa-state">
        Single-page app fallback is {site.spaFallback ? 'on' : 'off'}
      </p>
      <p className="text-xs text-text-tertiary">
        {site.spaFallback
          ? 'Unknown paths load index.html.'
          : 'Unknown paths answer with the nearest 404.html.'}
      </p>
    </section>
  );
}

function Routing({ projectId, site }: { readonly projectId: string; readonly site: Site }) {
  const update = useSetSpaFallback(projectId, site.id);
  // Held until the server answers, so the box follows the click at once.
  const [pending, setPending] = useState<boolean | null>(null);
  const change = (spaFallback: boolean) => {
    setPending(spaFallback);
    update.mutate(spaFallback, { onSettled: () => setPending(null) });
  };
  return (
    <section className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-2">
      <label className="flex items-start gap-3 text-sm text-text-primary">
        <input
          type="checkbox"
          checked={pending ?? site.spaFallback}
          disabled={pending !== null}
          onChange={(event) => change(event.target.checked)}
          className="mt-0.5"
          data-testid="site-spa-toggle"
        />
        <span>
          Single-page app fallback{' '}
          <span className="block text-xs text-text-tertiary" data-testid="site-spa-note">
            Unknown paths load index.html instead of your 404.html. A change applies from the next
            deploy; a rollback brings back the routing its deploy had.
          </span>
        </span>
      </label>
      {update.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(update.error, 'The setting could not be saved')}
        </p>
      )}
    </section>
  );
}

function DeleteSite({ projectId, site }: { readonly projectId: string; readonly site: Site }) {
  const navigate = useNavigate();
  const remove = useDeleteSite(projectId, site.id);
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={secondaryButton}
        data-testid="site-delete"
      >
        <Trash2 className="w-4 h-4" /> Delete
      </button>
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        onConfirm={() =>
          remove.mutateAsync().then(
            () => navigate(`/project/${projectId}/sites`),
            () => setOpen(false),
          )
        }
        title={`Delete ${site.name}?`}
        message={`${site.url} stops serving and every deploy of this site is removed. This cannot be undone.`}
        confirmLabel="Delete site"
        confirmText={site.name}
        destructive
        loading={remove.isPending}
      />
      {remove.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(remove.error, 'The site could not be deleted')}
        </p>
      )}
    </>
  );
}

function Detail({ projectId, siteId }: { readonly projectId: string; readonly siteId: string }) {
  const { data: site, error } = useSite(projectId, siteId);
  const { data: deploys = [] } = useSiteDeploys(projectId, siteId);
  const deploy = useDeploySite(projectId, siteId);
  const rollback = useRollbackSite(projectId, siteId);
  const { canDevelop, canAdmin } = useProjectRole(projectId);

  if (error) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load this site')}
      </div>
    );
  }
  if (!site) return <Spinner />;

  return (
    <>
      <SitesHeader
        title={site.name}
        subtitle={site.hostname}
        actions={
          <>
            <ToneBadge
              {...(site.liveDeployId
                ? { label: 'Live', tone: 'success' }
                : { label: 'Not deployed', tone: 'neutral' })}
              testId="site-status"
            />
            {canDevelop && <DeleteSite projectId={projectId} site={site} />}
          </>
        }
      />
      <p className="mb-4 text-sm">
        <a
          href={site.url}
          target="_blank"
          rel="noreferrer"
          className="text-purple-400 hover:underline"
          data-testid="site-url"
        >
          {site.url}
        </a>
      </p>
      <div className="grid gap-6 lg:grid-cols-2 mb-6">
        {canDevelop && (
          <section className="space-y-2">
            <h4 className="text-sm font-semibold text-text-primary">Deploy</h4>
            <SiteUpload
              deploying={deploy.isPending}
              onDeploy={(archive) => deploy.mutateAsync(archive)}
            />
          </section>
        )}
        <section className="space-y-2">
          <h4 className="text-sm font-semibold text-text-primary">Routing</h4>
          {canDevelop ? (
            <Routing projectId={projectId} site={site} />
          ) : (
            <RoutingState site={site} />
          )}
        </section>
      </div>
      <SiteGitBuilds projectId={projectId} siteId={siteId} canDevelop={canDevelop} canAdmin={canAdmin} />
      <section className="space-y-2 mb-6">
        <h4 className="text-sm font-semibold text-text-primary">Custom domains</h4>
        <SiteDomains projectId={projectId} siteId={siteId} canDevelop={canDevelop} />
      </section>
      <section className="space-y-2 mb-6">
        <h4 className="text-sm font-semibold text-text-primary">Deploy from CI</h4>
        <SiteCiSnippet projectId={projectId} siteId={siteId} />
      </section>
      {canDevelop && (
        <section className="space-y-2 mb-6">
          <h4 className="text-sm font-semibold text-text-primary">Deploy tokens</h4>
          <SiteDeployTokens projectId={projectId} siteId={siteId} />
        </section>
      )}
      <section className="space-y-2">
        <h4 className="text-sm font-semibold text-text-primary">Deployments</h4>
        {rollback.error && (
          <div
            role="alert"
            className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
          >
            {apiErrorMessage(rollback.error, 'The rollback failed')}
          </div>
        )}
        <div className="bg-surface-card border border-border-primary rounded-lg">
          <SiteDeployHistory
            deploys={deploys}
            rollingBack={rollback.isPending}
            onRollback={canDevelop ? (id) => rollback.mutate(id) : undefined}
          />
        </div>
      </section>
    </>
  );
}

export function SiteDetailPage() {
  const { projectId = '', siteId = '' } = useParams<{ projectId: string; siteId: string }>();
  return (
    <div data-testid="site-detail-page">
      <Link
        to={`/project/${projectId}/sites`}
        className="text-xs text-text-tertiary hover:text-text-primary"
      >
        ← Sites
      </Link>
      <div className="mt-3">
        <SitesGate projectId={projectId}>
          <Detail projectId={projectId} siteId={siteId} />
        </SitesGate>
      </div>
    </div>
  );
}
