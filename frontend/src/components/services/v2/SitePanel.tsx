import { apiErrorMessage } from '../../../api/apps';
import { useDeploySite, useRollbackSite, useSite, useSiteDeploys, type Site, type SiteDeploy } from '../../../api/sites';
import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { useProjectRole } from '../../../hooks/useProjectRole';
import { DeleteSite, Routing, RoutingState } from '../../../pages/SiteDetailPage';
import { Spinner } from '../../containers/ContainerBits';
import { SiteCiSnippet } from '../../sites/SiteCiSnippet';
import { SiteDeployHistory } from '../../sites/SiteDeployHistory';
import { SiteDeployTokens } from '../../sites/SiteDeployTokens';
import { SiteDomains } from '../../sites/SiteDomains';
import { SiteGitBuilds } from '../../sites/SiteGitBuilds';
import { SiteServerEnv } from '../../sites/SiteServerEnv';
import { SiteUpload } from '../../sites/SiteUpload';
import { siteCardStatus } from './cardStatus';
import { PanelFrame } from './PanelFrame';
import { tabsFor } from './panelState';

interface SiteProps {
  readonly projectId: string;
  readonly site: Site;
}

const Section = ({ title, children }: { readonly title: string; readonly children: React.ReactNode }) => (
  <section className="space-y-2">
    <h3 className="text-sm font-semibold text-text-primary">{title}</h3>
    {children}
  </section>
);

function Overview({ projectId, site }: SiteProps) {
  const deploy = useDeploySite(projectId, site.id);
  const { canDevelop } = useProjectRole(projectId);
  return (
    <div className="space-y-5" data-testid="site-overview">
      <p className="text-sm">
        <a href={site.url} target="_blank" rel="noreferrer" className="text-purple-400 hover:underline">{site.url}</a>
      </p>
      {canDevelop && (
        <Section title="Deploy">
          <SiteUpload deploying={deploy.isPending} onDeploy={(archive) => deploy.mutateAsync(archive)} />
        </Section>
      )}
      <Section title="Routing">{canDevelop ? <Routing projectId={projectId} site={site} /> : <RoutingState site={site} />}</Section>
    </div>
  );
}

function Deploys({ projectId, site, deploys }: SiteProps & { readonly deploys: readonly SiteDeploy[] }) {
  const rollback = useRollbackSite(projectId, site.id);
  const { canDevelop, canAdmin } = useProjectRole(projectId);
  return (
    <div className="space-y-6">
      <SiteGitBuilds projectId={projectId} siteId={site.id} canDevelop={canDevelop} canAdmin={canAdmin} serverSite={site.kind === 'server'} />
      <Section title="Deployments">
        {rollback.error && (
          <div role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400">
            {apiErrorMessage(rollback.error, 'The rollback failed')}
          </div>
        )}
        <div className="rounded-lg border border-border-primary bg-bg-secondary">
          <SiteDeployHistory deploys={[...deploys]} rollingBack={rollback.isPending} onRollback={canDevelop ? (id) => rollback.mutate(id) : undefined} />
        </div>
      </Section>
      <Section title="Deploy from CI">
        <SiteCiSnippet projectId={projectId} siteId={site.id} />
      </Section>
      {canDevelop && (
        <Section title="Deploy tokens">
          <SiteDeployTokens projectId={projectId} siteId={site.id} />
        </Section>
      )}
    </div>
  );
}

function Variables({ projectId, site }: SiteProps) {
  const { enabled: serverSitesOn } = useFeatureEnabled('sitesserver');
  const { canDevelop } = useProjectRole(projectId);
  if (site.kind === 'server' && serverSitesOn && canDevelop) return <SiteServerEnv projectId={projectId} siteId={site.id} />;
  return (
    <p className="text-sm text-text-secondary">
      {site.kind === 'server' ? 'Only a developer or an admin can change this site’s variables.' : 'A static site has no variables: it serves the files you deploy.'}
    </p>
  );
}

function SettingsTab({ projectId, site, onDeleted }: SiteProps & { readonly onDeleted: () => void }) {
  const { canDevelop } = useProjectRole(projectId);
  return (
    <div className="space-y-6">
      <Section title="Custom domains">
        <SiteDomains projectId={projectId} siteId={site.id} canDevelop={canDevelop} />
      </Section>
      {canDevelop && (
        <Section title="Delete site">
          <DeleteSite projectId={projectId} site={site} afterDelete={onDeleted} />
        </Section>
      )}
    </div>
  );
}

interface TabBodyProps extends SiteProps {
  readonly tab: string;
  readonly deploys: readonly SiteDeploy[];
  readonly onDeleted: () => void;
}

function TabBody({ tab, projectId, site, deploys, onDeleted }: TabBodyProps) {
  switch (tab) {
    case 'deploys':
      return <Deploys projectId={projectId} site={site} deploys={deploys} />;
    case 'variables':
      return <Variables projectId={projectId} site={site} />;
    case 'settings':
      return <SettingsTab projectId={projectId} site={site} onDeleted={onDeleted} />;
    default:
      return <Overview projectId={projectId} site={site} />;
  }
}

interface SitePanelProps {
  readonly projectId: string;
  readonly siteId: string;
  readonly tab: string;
  readonly onTab: (key: string) => void;
  readonly onClose: () => void;
}

export function SitePanel({ projectId, siteId, tab, onTab, onClose }: SitePanelProps) {
  const { data: site, error } = useSite(projectId, siteId);
  const { data: deploys = [] } = useSiteDeploys(projectId, siteId);
  if (error || !site) {
    return (
      <PanelFrame title="Site" onClose={onClose} idPrefix="site">
        {error ? <p role="alert" className="text-sm text-red-400">{apiErrorMessage(error, 'Could not load this site')}</p> : <Spinner />}
      </PanelFrame>
    );
  }
  return (
    <PanelFrame title={site.name} subtitle={site.hostname} status={siteCardStatus(site, deploys)} tabsLabel="Site" tabs={tabsFor('site')} tab={tab} onTab={onTab} onClose={onClose} idPrefix="site">
      <TabBody projectId={projectId} site={site} deploys={deploys} tab={tab} onDeleted={onClose} />
    </PanelFrame>
  );
}
