import { Link, useParams } from 'react-router-dom';
import { Plus } from 'lucide-react';
import { apiErrorMessage } from '../api/apps';
import { useSiteLimits, useSites, type Site } from '../api/sites';
import { useSitesEnabled } from '../hooks/useDeploymentMode';
import { useProjectRole } from '../hooks/useProjectRole';
import { Spinner, ToneBadge, primaryButton } from '../components/containers/ContainerBits';
import { SiteLimitsNote, SitesGate, SitesHeader } from '../components/sites/SiteBits';

function SiteRow({ projectId, site }: { readonly projectId: string; readonly site: Site }) {
  return (
    <div
      className="flex items-center justify-between gap-4 px-4 py-3 bg-surface-card border border-border-primary rounded-lg"
      data-testid={`site-row-${site.id}`}
    >
      <div className="min-w-0 space-y-0.5">
        <Link
          to={`/project/${projectId}/sites/${site.id}`}
          className="text-sm font-medium text-text-primary hover:text-purple-400"
        >
          {site.name}
        </Link>
        <a
          href={site.url}
          target="_blank"
          rel="noreferrer"
          className="block text-xs text-purple-400 hover:underline truncate"
        >
          {site.url}
        </a>
      </div>
      <ToneBadge
        {...(site.liveDeployId
          ? { label: 'Live', tone: 'success' }
          : { label: 'Not deployed', tone: 'neutral' })}
        testId={`site-status-${site.id}`}
      />
    </div>
  );
}

function EmptyState({
  projectId,
  canDevelop,
}: {
  readonly projectId: string;
  readonly canDevelop: boolean;
}) {
  return (
    <div
      className="bg-surface-card border border-border-primary rounded-lg p-8 text-center"
      data-testid="sites-empty"
    >
      <p className="text-sm text-text-primary font-medium">No sites yet</p>
      <p className="text-sm text-text-secondary mt-2 max-w-lg mx-auto">
        A site serves your frontend&apos;s build output (Vite, Astro, a static Next export, plain
        HTML) from a global CDN. For a server-rendered app, use a container instead.
      </p>
      {canDevelop && (
        <div className="mt-5 flex justify-center">
          <Link to={`/project/${projectId}/sites/new`} className={primaryButton}>
            <Plus className="w-4 h-4" /> New site
          </Link>
        </div>
      )}
    </div>
  );
}

function SitesList({
  projectId,
  canDevelop,
}: {
  readonly projectId: string;
  readonly canDevelop: boolean;
}) {
  const { data: sites, error } = useSites(projectId);
  const { data: limits } = useSiteLimits(projectId);
  if (error) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load the sites')}
      </div>
    );
  }
  if (!sites) return <Spinner />;
  return (
    <>
      {sites.length === 0 ? (
        <EmptyState projectId={projectId} canDevelop={canDevelop} />
      ) : (
        <div className="space-y-2" data-testid="sites-list">
          {sites.map((site) => (
            <SiteRow key={site.id} projectId={projectId} site={site} />
          ))}
        </div>
      )}
      {limits && <SiteLimitsNote limits={limits} held={sites.length} />}
    </>
  );
}

export function SitesPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { enabled } = useSitesEnabled();
  const { canDevelop } = useProjectRole(projectId);
  return (
    <div data-testid="sites-page">
      <SitesHeader
        title="Sites"
        subtitle="Your frontend's build output, served from a global CDN."
        actions={
          enabled &&
          canDevelop && (
            <Link
              to={`/project/${projectId}/sites/new`}
              className={primaryButton}
              data-testid="sites-new"
            >
              <Plus className="w-4 h-4" /> New
            </Link>
          )
        }
      />
      <SitesGate projectId={projectId}>
        <SitesList projectId={projectId} canDevelop={canDevelop} />
      </SitesGate>
    </div>
  );
}
